// Copyright (c) 2019, AT&T Intellectual Property. All rights reseved.
//
// SPDX-License-Identifier: GPL-2.0-only
package notifyd

import (
	"bytes"
	"encoding/json"
	"io/ioutil"
	"log"
	"log/syslog"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/danos/utils/args"
	"github.com/go-ini/ini"
	"jsouthworth.net/go/etm/agent"
	"jsouthworth.net/go/etm/atom"
	"jsouthworth.net/go/immutable/hashmap"
	"jsouthworth.net/go/immutable/hashset"
	"jsouthworth.net/go/seq"
	"jsouthworth.net/go/transduce"
)

var (
	elog *log.Logger
	dlog *log.Logger
)

func init() {
	var err error
	elog, err = syslog.NewLogger(syslog.LOG_ERR, 0)
	if err != nil {
		elog = log.New(os.Stderr, "", 0)
	}
	dlog, err = syslog.NewLogger(syslog.LOG_DEBUG, 0)
	if err != nil {
		dlog = log.New(os.Stdout, "", 0)
	}
}

type Stats struct {
	NumRegisteredHandlers uint64
	NumScriptsExecd       uint64
	NumWorkers            uint32
	StaticHandlers        *hashmap.Map
	TransientHandlers     *hashmap.Map
	CollapsedHandlers     *hashmap.Map
}

type atomicStats struct {
	numRegisteredHandlers int64
	numScriptsExecd       uint64
}

type Notifier struct {
	opts  *options
	stats atomicStats

	staticSubs    *atom.Atom
	transientSubs *atom.Atom

	subs    *agent.Agent
	vciSubs *agent.Agent

	worker *agent.Agent
}

func (n *Notifier) When(module, event, script string) {
	n.transientSubs.Swap(func(m *hashmap.Map) *hashmap.Map {
		var scripts *hashset.Set
		key := makeNotificationKey(module, event)
		vals, ok := m.Find(key)
		if !ok {
			scripts = hashset.Empty()
		} else {
			scripts = vals.(*hashset.Set)
		}
		return m.Assoc(key, scripts.Add(script))
	})
}

func (n *Notifier) Done(module, event, script string) {
	n.transientSubs.Swap(func(m *hashmap.Map) *hashmap.Map {
		key := makeNotificationKey(module, event)
		vals, ok := m.Find(key)
		if !ok {
			return m
		}
		scripts := vals.(*hashset.Set)
		scripts = scripts.Delete(script)
		if scripts.Length() == 0 {
			return m.Delete(key)
		}
		return m.Assoc(key, scripts)
	})
}

func (n *Notifier) Stats() *Stats {
	handlers := n.subs.Deref().(*hashmap.Map)
	return &Stats{
		NumRegisteredHandlers: countHandlers(handlers),
		NumScriptsExecd:       atomic.LoadUint64(&n.stats.numScriptsExecd),
		NumWorkers:            n.opts.nWorker,
		StaticHandlers:        n.staticSubs.Deref().(*hashmap.Map),
		TransientHandlers:     n.transientSubs.Deref().(*hashmap.Map),
		CollapsedHandlers:     handlers,
	}
}

func (n *Notifier) dispatch(module, name, input string) {
	subs := n.subs.Deref().(*hashmap.Map)
	notif := makeNotificationKey(module, name)
	val, ok := subs.Find(notif)
	if !ok {
		return
	}
	jobs := val.(*hashset.Set)
	n.worker.Send((*worker).exec, jobs, module, name, input)
}

func (n *Notifier) writeSubs(subs *hashmap.Map) {
	m := subsToMap(subs)
	if len(m) == 0 {
		os.Remove(n.opts.eventCache)
		return
	}
	f, err := os.OpenFile(n.opts.eventCache,
		os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		elog.Println(err)
		return
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	err = enc.Encode(m)
	if err != nil {
		elog.Println(err)
		return
	}
}

// appendingConjoiner implements a conjoin method for a transient map that
// concatenates the elements stored at the keys
type appendingConjoiner struct {
	m *hashmap.TMap
}

func (m *appendingConjoiner) Conj(in interface{}) interface{} {
	elem := in.(hashmap.Entry)
	val, ok := m.m.Find(elem.Key())
	if !ok {
		return &appendingConjoiner{m.m.Assoc(elem.Key(), elem.Value())}
	}
	return &appendingConjoiner{m.m.Assoc(elem.Key(), seq.Into(val, elem.Value()))}
}

func subsToMap(in *hashmap.Map) map[string][]string {
	out := make(map[string][]string)
	in.Range(func(k string, v *hashset.Set) {
		out[k] = scriptsToSlice(v)
	})
	return out
}

func scriptsToSlice(in *hashset.Set) []string {
	out := make([]string, 0, in.Length())
	in.Range(func(script string) {
		out = append(out, script)
	})
	return out
}

type Subscriber interface {
	Subscribe(module, event string, sub interface{}) Subscription
}

type Subscription interface {
	Run() error
	Cancel() error
}

type Option func(*options)

type options struct {
	eventCache string
	staticDir  string
	nWorker    uint32
	subscriber Subscriber
}

func EventCache(filename string) Option {
	return func(opts *options) {
		opts.eventCache = filename
	}
}

func StaticDir(directory string) Option {
	return func(opts *options) {
		opts.staticDir = directory
	}
}

func SubscribeWith(sub Subscriber) Option {
	return func(opts *options) {
		opts.subscriber = sub
	}
}

func ConcurrentScripts(numConcurrent uint32) Option {
	return func(opts *options) {
		opts.nWorker = numConcurrent
	}
}

func New(opts ...Option) *Notifier {
	os := &options{
		eventCache: "/run/vci/notifyd.cache",
		staticDir:  "/lib/vci/notify/static",
		nWorker:    uint32(runtime.NumCPU() * 2),
	}
	for _, opt := range opts {
		opt(os)
	}

	if os.subscriber == nil {
		panic("Must provide a mechanism with which to subscribe to messages")
	}

	n := &Notifier{
		opts: os,

		transientSubs: atom.New(hashmap.Empty()),
		staticSubs:    atom.New(hashmap.Empty()),

		subs:    agent.New(hashmap.Empty()),
		vciSubs: agent.New(hashmap.Empty()),
	}
	n.worker = agent.New(workerNew(n))

	updateSubs := func(_, _ interface{}, old, new *hashmap.Map, s1 *atom.Atom) {
		n.subs.Send(func(_, s1, s2 *hashmap.Map) *hashmap.Map {
			return collapseSubscriptions(s1, s2)
		}, new, s1.Deref())
	}
	n.transientSubs.Watch("update-subs", updateSubs, n.staticSubs)
	n.staticSubs.Watch("update-subs", updateSubs, n.transientSubs)

	updateVCISubs := func(_, _ interface{}, old, new *hashmap.Map) {
		n.vciSubs.Send(updateVCISubscriptions, new, n)
	}
	n.subs.Watch("update-vci", updateVCISubs)

	// TODO: Watch /lib/vci/notify/ for changes
	//       Update staticSubs on change.
	n.staticSubs.Swap(readStaticSubs, n.opts.staticDir)

	n.transientSubs.Swap(func(_ *hashmap.Map, new map[string][]string) *hashmap.Map {
		return mapToSubscriptions(new)
	}, readCacheFile(n.opts.eventCache))
	// Note: This should come after the load of the cache for
	//       efficiencies sake.
	writeSubs := func(_, _ interface{}, _, new *hashmap.Map) {
		n.writeSubs(new)
	}
	n.transientSubs.Watch("cache-subs", writeSubs)

	return n
}

func collapseSubscriptions(subs ...*hashmap.Map) *hashmap.Map {
	return seq.TransformInto(
		&appendingConjoiner{hashmap.Empty().AsTransient()},
		transduce.Compose(
			transduce.Cat(seq.Reduce),
			transduce.Map(func(entry hashmap.Entry) hashmap.Entry {
				return hashmap.EntryNew(
					entry.Key(),
					seq.Into(hashset.Empty(), entry.Value()),
				)
			}),
		),
		subs,
	).(*appendingConjoiner).m.AsPersistent()
}

func updateVCISubscriptions(vciSubs, subs *hashmap.Map, n *Notifier) *hashmap.Map {
	return vciSubs.Transform(func(m *hashmap.TMap) *hashmap.TMap {
		vciSubs.Range(func(k string, sub Subscription) {
			subs, exists := subs.Find(k)
			if !exists || subs.(*hashset.Set).Length() == 0 {
				dlog.Println("Unsubscribing from", k)
				err := sub.Cancel()
				if err == nil {
					m.Delete(k)
				} else {
					elog.Println("Error when canceling subscription:",
						k, ":", err)
				}
			}
		})
		subs.Range(func(k string, set *hashset.Set) {
			if !vciSubs.Contains(k) && set.Length() > 0 {
				module, event := parseNotificationKey(k)
				log.Println("Subscribing to", k)
				sub := n.opts.subscriber.Subscribe(module, event,
					func(input string) {
						n.dispatch(module, event, input)
					})
				err := sub.Run()
				if err == nil {
					m.Assoc(k, sub)
				} else {
					elog.Println("Error when subscribing:",
						k, ":", err)
				}
			}
		})
		return m
	})
}

func readStaticSubs(old *hashmap.Map, staticDir string) *hashmap.Map {
	dir, err := ioutil.ReadDir(staticDir)
	if err != nil {
		return hashmap.Empty()
	}
	return iniFilesToSubscriptions(
		dir,
		func(name string) string {
			return staticDir + "/" + name
		},
		readStaticSub,
	)
}

func readStaticSub(filename string) *ini.File {
	cfg, err := ini.Load(filename)
	if err != nil {
		elog.Println(filename, err)
		return ini.Empty()
	}
	return cfg
}

func iniFilesToSubscriptions(
	dir []os.FileInfo,
	filenameMapper func(string) string,
	readINI func(name string) *ini.File,
) *hashmap.Map {
	return seq.TransformInto(
		&appendingConjoiner{hashmap.Empty().AsTransient()},
		transduce.Compose(
			transduce.Remove(func(fi os.FileInfo) bool {
				return fi.IsDir()
			}),
			transduce.Filter(func(fi os.FileInfo) bool {
				return strings.HasSuffix(fi.Name(), ".notify")
			}),
			transduce.Map(func(fi os.FileInfo) *hashmap.Map {
				return iniToSubscriptions(
					readINI(filenameMapper(fi.Name())))
			}),
			transduce.Cat(seq.Reduce),
		),
		dir,
	).(*appendingConjoiner).m.AsPersistent()
}

func iniToSubscriptions(cfg *ini.File) *hashmap.Map {
	// [When module:name]
	// script=a --args, b --args, c --args

	//TODO: update to allow 'shadows' for multiple scripts when INI library is
	//      updated in debian.
	return seq.TransformInto(
		&appendingConjoiner{hashmap.Empty().AsTransient()},
		transduce.Compose(
			transduce.Filter(func(section *ini.Section) bool {
				return strings.HasPrefix(section.Name(), "When")
			}),
			transduce.Filter(func(section *ini.Section) bool {
				return len(strings.Split(section.Name(), " ")) == 2
			}),
			transduce.Map(func(section *ini.Section) hashmap.Entry {
				return hashmap.EntryNew(
					strings.Split(section.Name(), " ")[1],
					hashset.From(section.Key("script").Strings(",")))
			}),
		),
		cfg.Sections(),
	).(*appendingConjoiner).m.AsPersistent()
}

func mapToSubscriptions(new map[string][]string) *hashmap.Map {
	return seq.TransformInto(
		hashmap.Empty(),
		transduce.Compose(
			transduce.Filter(func(entry seq.MapEntry) bool {
				return len(strings.Split(entry.Key().(string), ":")) == 2
			}),
			transduce.Map(func(entry seq.MapEntry) hashmap.Entry {
				return hashmap.EntryNew(entry.Key(),
					hashset.From(entry.Value()))
			}),
		),
		new,
	).(*hashmap.Map)
}

func readCacheFile(filename string) map[string][]string {
	f, err := os.Open(filename)
	if err != nil {
		// ignore file not exist error, it will only exist
		// when there are active subscriptions
		if !os.IsNotExist(err) {
			elog.Println("error reading subscriptions", err)
		}
		return nil
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	m := make(map[string][]string)
	err = dec.Decode(&m)
	if err != nil {
		elog.Println("error reading subscriptions", err)
		return nil
	}
	return m
}

func countHandlers(handlers *hashmap.Map) uint64 {
	return seq.Reduce(func(res uint64, entry hashmap.Entry) uint64 {
		return res + uint64(entry.Value().(*hashset.Set).Length())
	}, uint64(0), handlers).(uint64)
}

func makeNotificationKey(module, event string) string {
	return module + ":" + event
}

func parseNotificationKey(key string) (string, string) {
	vals := strings.SplitN(key, ":", 2)
	switch len(vals) {
	case 1:
		return "", vals[0]
	default:
		return vals[0], vals[1]
	}
}

type worker struct {
	sem        chan struct{}
	n          *Notifier
	readerPool sync.Pool
	bufPool    sync.Pool
	envPool    sync.Pool
	env        []string
}

func workerNew(n *Notifier) *worker {
	env := os.Environ()
	return &worker{
		sem: make(chan struct{}, int(n.opts.nWorker)),
		n:   n,
		readerPool: sync.Pool{
			New: func() interface{} {
				return new(strings.Reader)
			},
		},
		bufPool: sync.Pool{
			New: func() interface{} {
				return new(bytes.Buffer)
			},
		},
		envPool: sync.Pool{
			New: func() interface{} {
				out := make([]string, len(env)+2)
				copy(out, env)
				return out
			},
		},
		env: env,
	}
}

// The 'exec' method runs a loop over the a set of handlers using a counting
// semaphore to ensure that jobs executed concurrently are limited to
// the number of desired concurrent workers. This is done instead of
// maintaining a worker pool.
func (w *worker) exec(jobs *hashset.Set, module, name, input string) *worker {
	jobs.Range(func(script string) {
		w.sem <- struct{}{}
		go func() {
			w.job(script, module, name, input)
			<-w.sem
		}()
		atomic.AddUint64(&w.n.stats.numScriptsExecd, 1)
	})
	return w
}

func (w *worker) job(script, module, name, notif string) {
	as := args.ParseArgs(script)
	stdin := w.readerPool.Get().(*strings.Reader)
	stdin.Reset(notif)
	defer w.readerPool.Put(stdin)
	stdout := w.bufPool.Get().(*bytes.Buffer)
	stdout.Reset()
	defer w.bufPool.Put(stdout)
	stderr := w.bufPool.Get().(*bytes.Buffer)
	stderr.Reset()
	defer w.bufPool.Put(stderr)
	env := w.envPool.Get().([]string)
	env[len(w.env)] = "NOTIFYD_MODULE=" + module
	env[len(w.env)+1] = "NOTIFYD_NOTIF=" + name
	defer w.envPool.Put(env)

	cmd := exec.Command(as[0], as[1:]...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env = env

	err := cmd.Run()
	if stdout.Len() != 0 {
		dlog.Printf("Output for %s:%s(%s):\n%s\n",
			module, name, script, stdout.String())
	}

	if err == nil {
		return
	}

	if stderr.Len() == 0 {
		elog.Printf("Error when running %s:%s(%s): %s\n",
			module, name, script, err)
		return
	}

	elog.Printf("Error when running %s:%s(%s):\n%s\n",
		module, name, script, stderr.String())
}
