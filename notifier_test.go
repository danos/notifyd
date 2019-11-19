// Copyright (c) 2019, AT&T Intellectual Property. All rights reseved.
//
// SPDX-License-Identifier: GPL-2.0-only
package notifyd

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/go-ini/ini"
	"jsouthworth.net/go/dyn"
	"jsouthworth.net/go/immutable/hashmap"
	"jsouthworth.net/go/immutable/hashset"
)

func assertEqual(t *testing.T, v1, v2 interface{}) {
	if dyn.Equal(v1, v2) {
		return
	}
	t.Fatalf("got:\n%v\n\nexpected:\n%v\n", v1, v2)
}

func TestCollapseSubscriptions(t *testing.T) {
	t.Run("disjoint subscriptions", func(t *testing.T) {
		out := collapseSubscriptions(
			hashmap.Empty().
				Assoc("foo:bar", hashset.New("foo")),
			hashmap.Empty().
				Assoc("bar:baz", hashset.New("bar")))
		assertEqual(t, out.At("foo:bar").(*hashset.Set).At("foo"), "foo")
		assertEqual(t, out.At("bar:baz").(*hashset.Set).At("bar"), "bar")
	})
	t.Run("overlapping subscriptions", func(t *testing.T) {
		out := collapseSubscriptions(
			hashmap.Empty().
				Assoc("foo:bar", hashset.New("foo")),
			hashmap.Empty().
				Assoc("foo:bar", hashset.New("bar")))
		assertEqual(t, out.At("foo:bar").(*hashset.Set).Length(), 2)
		assertEqual(t, out.At("foo:bar").(*hashset.Set).At("foo"), "foo")
		assertEqual(t, out.At("foo:bar").(*hashset.Set).At("bar"), "bar")
	})
	t.Run("overlapping disjoint subscriptions", func(t *testing.T) {
		out := collapseSubscriptions(
			hashmap.Empty().
				Assoc("foo:bar", hashset.New("foo")).
				Assoc("foo:baz", hashset.New("bar")),
			hashmap.Empty().
				Assoc("foo:bar", hashset.New("bar")).
				Assoc("foo:baz", hashset.New("baz")),
		)
		assertEqual(t, out.At("foo:bar").(*hashset.Set).Length(), 2)
		assertEqual(t, out.At("foo:bar").(*hashset.Set).At("foo"), "foo")
		assertEqual(t, out.At("foo:bar").(*hashset.Set).At("bar"), "bar")

		assertEqual(t, out.At("foo:baz").(*hashset.Set).Length(), 2)
		assertEqual(t, out.At("foo:baz").(*hashset.Set).At("bar"), "bar")
		assertEqual(t, out.At("foo:baz").(*hashset.Set).At("baz"), "baz")
	})
	t.Run("conflicting subscriptions", func(t *testing.T) {
		out := collapseSubscriptions(
			hashmap.Empty().
				Assoc("foo:bar", hashset.New("foo")),
			hashmap.Empty().
				Assoc("foo:bar", hashset.New("foo")))
		assertEqual(t, out.At("foo:bar").(*hashset.Set).Length(), 1)
	})
	t.Run("conflicting disjoint subscriptions", func(t *testing.T) {
		out := collapseSubscriptions(
			hashmap.Empty().
				Assoc("foo:bar", hashset.New("foo")).
				Assoc("foo:baz", hashset.New("baz")),
			hashmap.Empty().
				Assoc("foo:bar", hashset.New("foo")).
				Assoc("foo:baz", hashset.New("baz")),
		)
		assertEqual(t, out.At("foo:bar").(*hashset.Set).Length(), 1)
		assertEqual(t, out.At("foo:baz").(*hashset.Set).Length(), 1)
	})
}

func TestMapToSubscriptions(t *testing.T) {
	t.Run("correct input", func(t *testing.T) {
		out := mapToSubscriptions(map[string][]string{
			"foo:bar": []string{"baz", "quux"},
			"foo:baz": []string{"quux", "foo"},
		})
		assertEqual(t, out.At("foo:bar"), hashset.New("baz", "quux"))
		assertEqual(t, out.At("foo:baz"), hashset.New("quux", "foo"))
	})
	t.Run("invalid key", func(t *testing.T) {
		out := mapToSubscriptions(map[string][]string{
			"foo":     []string{"baz", "quux"},
			"foo:baz": []string{"quux", "foo"},
		})
		assertEqual(t, out.At("foo"), nil)
		assertEqual(t, out.At("foo:baz"), hashset.New("quux", "foo"))
	})
	t.Run("nil", func(t *testing.T) {
		out := mapToSubscriptions(nil)
		assertEqual(t, out.Length(), 0)
	})
}

func TestSubsToMap(t *testing.T) {
	subs := hashmap.New(
		"foo:bar", hashset.New("foo", "bar"),
		"baz:quux", hashset.New("baz", "quux"),
	)
	m := subsToMap(subs)
	exp := map[string][]string{
		"foo:bar":  []string{"foo", "bar"},
		"baz:quux": []string{"baz", "quux"},
	}
	for k, v := range exp {
		scripts, ok := m[k]
		if !ok {
			t.Fatalf("got:\n%s\n\nexpected:\n%s\n", m, exp)
		}
		for _, script := range v {
			var found bool
			for _, sc := range scripts {
				if sc == script {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("got:\n%s\n\nexpected:\n%s\n", m, exp)
			}
		}
	}
}

func TestCountHandlers(t *testing.T) {
	handlers := hashmap.New(
		"foo:bar", hashset.New("foo", "bar"),
		"baz:quux", hashset.New("baz", "quux"),
	)
	assertEqual(t, countHandlers(handlers), uint64(4))
}

func TestMakeNotificationKey(t *testing.T) {
	k := makeNotificationKey("foo", "bar")
	assertEqual(t, k, "foo:bar")
}

func TestParseNotificationKey(t *testing.T) {
	t.Run("correct input", func(t *testing.T) {
		mod, name := parseNotificationKey("foo:bar")
		assertEqual(t, mod, "foo")
		assertEqual(t, name, "bar")
	})
	t.Run("no module", func(t *testing.T) {
		mod, name := parseNotificationKey("bar")
		assertEqual(t, mod, "")
		assertEqual(t, name, "bar")
	})
	t.Run("many colons", func(t *testing.T) {
		mod, name := parseNotificationKey("foo:bar:baz")
		assertEqual(t, mod, "foo")
		assertEqual(t, name, "bar:baz")
	})
}

func TestIniToSubscriptions(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		out := iniToSubscriptions(ini.Empty())
		assertEqual(t, out.Length(), 0)
	})
	t.Run("bogus section skipped", func(t *testing.T) {
		cfg := ini.Empty()
		sec, err := cfg.NewSection("When foo bar")
		if err != nil {
			t.Fatal(err)
		}
		sec.NewKey("scripts", "foo")
		out := iniToSubscriptions(cfg)
		assertEqual(t, out.Length(), 0)
	})
	t.Run("single script", func(t *testing.T) {
		cfg := ini.Empty()
		sec, err := cfg.NewSection("When foo:bar")
		if err != nil {
			t.Fatal(err)
		}
		sec.NewKey("script", "foo")
		out := iniToSubscriptions(cfg)
		assertEqual(t, countHandlers(out), uint64(1))
	})
	t.Run("multiple script", func(t *testing.T) {
		cfg := ini.Empty()
		sec, err := cfg.NewSection("When foo:bar")
		if err != nil {
			t.Fatal(err)
		}
		sec.NewKey("script", "foo,bar,baz")
		out := iniToSubscriptions(cfg)
		assertEqual(t, countHandlers(out), uint64(3))
	})
	t.Run("multiple when sections", func(t *testing.T) {
		out := iniToSubscriptions(ini.Empty())
		assertEqual(t, out.Length(), 0)
	})
}

type mockFileInfo struct {
	name  string
	isDir bool
}

func (fi *mockFileInfo) Name() string {
	return fi.name
}
func (fi *mockFileInfo) Size() int64 {
	return 0
}
func (fi *mockFileInfo) Mode() os.FileMode {
	return 0777
}
func (fi *mockFileInfo) ModTime() time.Time {
	return time.Now()
}
func (fi *mockFileInfo) IsDir() bool {
	return fi.isDir
}
func (fi *mockFileInfo) Sys() interface{} {
	return nil
}

func getFile(name string, dir []os.FileInfo) os.FileInfo {
	for _, fi := range dir {
		if fi.Name() == name {
			return fi
		}
	}
	return nil
}

func genIniReader(files map[string]*ini.File) func(name string) *ini.File {
	return func(name string) *ini.File {
		return files[name]
	}
}

func emptyIniReader(name string) *ini.File {
	return ini.Empty()
}

func testNameMapper(name string) string {
	return name
}

func TestIniFilesToSubscriptions(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		out := iniFilesToSubscriptions(
			[]os.FileInfo{},
			testNameMapper,
			emptyIniReader,
		)
		assertEqual(t, out.Length(), 0)
	})
	t.Run("skips dirs", func(t *testing.T) {
		out := iniFilesToSubscriptions(
			[]os.FileInfo{&mockFileInfo{"foo", true}},
			testNameMapper,
			emptyIniReader,
		)
		assertEqual(t, out.Length(), 0)
	})
	t.Run("skips invalid names", func(t *testing.T) {
		out := iniFilesToSubscriptions(
			[]os.FileInfo{&mockFileInfo{"foo", false}},
			testNameMapper,
			emptyIniReader,
		)
		assertEqual(t, out.Length(), 0)
	})
	t.Run("reads single", func(t *testing.T) {
		file := ini.Empty()
		sec, err := file.NewSection("When foo:bar")
		if err != nil {
			t.Fatal(err)
		}
		sec.NewKey("script", "foo")
		out := iniFilesToSubscriptions(
			[]os.FileInfo{&mockFileInfo{"foo.notify", false}},
			testNameMapper,
			genIniReader(map[string]*ini.File{
				"foo.notify": file,
			}),
		)
		assertEqual(t, countHandlers(out), uint64(1))
	})
	t.Run("reads multiple", func(t *testing.T) {
		fooFile := ini.Empty()
		sec, err := fooFile.NewSection("When foo:bar")
		if err != nil {
			t.Fatal(err)
		}
		sec.NewKey("script", "foo")

		barFile := ini.Empty()
		sec, err = barFile.NewSection("When bar:baz")
		if err != nil {
			t.Fatal(err)
		}
		sec.NewKey("script", "bar")

		out := iniFilesToSubscriptions(
			[]os.FileInfo{&mockFileInfo{"foo.notify", false},
				&mockFileInfo{"bar.notify", false}},
			testNameMapper,
			genIniReader(map[string]*ini.File{
				"foo.notify": fooFile,
				"bar.notify": barFile,
			}),
		)
		assertEqual(t, countHandlers(out), uint64(2))
	})
}

type testSubDescription struct {
	action, module, event string
}

type testSubscriber struct {
	mu     sync.Mutex
	events chan testSubDescription
	subs   map[string]*testSubscription
}

func newTestSubscriber() *testSubscriber {
	return &testSubscriber{
		subs:   make(map[string]*testSubscription),
		events: make(chan testSubDescription, 1),
	}
}

func (s *testSubscriber) EavesDrop() chan testSubDescription {
	return s.events
}

func (s *testSubscriber) Subscribe(module, event string, sub interface{}) Subscription {
	s.mu.Lock()
	defer s.mu.Unlock()
	tsub := newTestSub(sub)
	s.subs[module+":"+event] = tsub
	s.events <- testSubDescription{action: "subscribe", module: module, event: event}
	return tsub
}

func (s *testSubscriber) Send(module, event, input string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.subs[module+":"+event]
	if !ok {
		return
	}
	sub.input <- input
}

func (s *testSubscriber) WaitCh(module, event string) chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.subs[module+":"+event]
	if !ok {
		return nil
	}
	return sub.done
}

func newTestSub(sub interface{}) *testSubscription {
	return &testSubscription{
		sub:   sub,
		input: make(chan string, 1),
		done:  make(chan struct{}),
	}
}

type testSubscription struct {
	sub   interface{}
	input chan string
	done  chan struct{}
}

func (s *testSubscription) Run() error {
	go func() {
		for {
			select {
			case in := <-s.input:
				dyn.Apply(s.sub, in)
			case <-s.done:
				return
			}
		}
	}()
	return nil
}

func (s *testSubscription) Cancel() error {
	close(s.done)
	return nil
}

func setupFileWatcher(t *testing.T, name string) *fsnotify.Watcher {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal("error creating watcher", err)
	}
	err = watcher.Add(filepath.Dir(name))
	if err != nil {
		t.Fatal("error adding file", err)
	}
	return watcher
}

func waitForFile(t *testing.T, name string, watcher *fsnotify.Watcher) {
	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				t.Fatal("file not created")
			}
			switch event.Op {
			case fsnotify.Write:
				if event.Name == name {
					return
				}
			}
		case <-time.After(5 * time.Second):
			// 5 seconds is way longer than any of these
			// operations should ever take.
			t.Fatal("timeout waiting for", name)
		}

	}
}

func waitForFileRemoval(t *testing.T, name string, watcher *fsnotify.Watcher) {
	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				t.Fatal("file not removed", name)
			}
			switch event.Op {
			case fsnotify.Remove:
				if event.Name == name {
					return
				}
			}
		case <-time.After(5 * time.Second):
			// 5 seconds is way longer than any of these
			// operations should ever take.
			t.Fatal("timeout waiting for", name)
		}
	}
}

func TestNotifier(t *testing.T) {
	// This test ensures the mechanics for the
	// notifier works as expected.
	dir, err := ioutil.TempDir("testdata", "tmp")
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		os.RemoveAll(dir)
	}()

	cacheFile := filepath.Join(dir, "notifyd.cache")
	bus := newTestSubscriber()
	notifyd := New(EventCache(cacheFile),
		StaticDir("testdata/static"),
		ConcurrentScripts(1),
		SubscribeWith(bus))

	subDesc := <-bus.EavesDrop()
	if subDesc.module != "foo" || subDesc.event != "bar" {
		t.Fatalf("unexpeted subscription %#v\n", subDesc)
	}

	t.Run("When", func(t *testing.T) {
		var start, end sync.WaitGroup
		start.Add(1)
		end.Add(1)
		go func() {
			watch := setupFileWatcher(t, cacheFile)
			start.Done()
			waitForFile(t, cacheFile, watch)
			subDesc := <-bus.EavesDrop()
			if subDesc.module != "bar" || subDesc.event != "baz" {
				t.Fatalf("unexpeted subscription %#v\n", subDesc)
			}
			end.Done()
		}()
		start.Wait()
		notifyd.When("bar", "baz", "sh testdata/scripts/callme")
		end.Wait()
	})

	t.Run("static notification send", func(t *testing.T) {
		foobarFile := filepath.Join(dir, "foobar.out")
		var start, end sync.WaitGroup
		start.Add(1)
		end.Add(1)
		go func() {
			watch := setupFileWatcher(t, foobarFile)
			start.Done()
			waitForFile(t, foobarFile, watch)
			end.Done()
		}()
		start.Wait()
		bus.Send("foo", "bar", foobarFile)
		end.Wait()
	})

	t.Run("transient notification send", func(t *testing.T) {
		barbazFile := filepath.Join(dir, "barbaz.out")
		var start, end sync.WaitGroup
		start.Add(1)
		end.Add(1)
		go func() {
			watch := setupFileWatcher(t, barbazFile)
			start.Done()
			waitForFile(t, barbazFile, watch)
			end.Done()
		}()
		start.Wait()
		bus.Send("bar", "baz", barbazFile)
		end.Wait()
	})

	t.Run("Done", func(t *testing.T) {
		var start, end sync.WaitGroup
		start.Add(1)
		end.Add(1)
		go func() {
			watch := setupFileWatcher(t, cacheFile)
			start.Done()
			waitForFileRemoval(t, cacheFile, watch)
			end.Done()
		}()
		start.Wait()
		notifyd.Done("bar", "baz", "sh testdata/scripts/callme")
		end.Wait()
		select {
		case <-bus.WaitCh("bar", "baz"):
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for subscription cancelation")
		}
	})
	t.Run("Stats", func(t *testing.T) {
		stats := notifyd.Stats()
		assertEqual(t, stats.NumRegisteredHandlers, uint64(1))
		assertEqual(t, stats.NumScriptsExecd, uint64(2))
		assertEqual(t, stats.NumWorkers, uint32(1))
	})
}
