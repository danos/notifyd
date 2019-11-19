// Copyright (c) 2019, AT&T Intellectual Property. All rights reseved.
//
// SPDX-License-Identifier: GPL-2.0-only
package main

import (
	"log"
	"strings"

	"github.com/danos/encoding/rfc7951/data"
	"github.com/danos/notifyd"
	"github.com/danos/vci"
	"jsouthworth.net/go/immutable/hashmap"
	"jsouthworth.net/go/immutable/hashset"
)

type rpcs struct {
	notifier *notifyd.Notifier
}

func (r *rpcs) When(input struct {
	Module       string `rfc7951:"notifyd-v1:module"`
	Notification string `rfc7951:"notifyd-v1:notification"`
	Script       string `rfc7951:"notifyd-v1:script"`
}) (struct{}, error) {
	r.notifier.When(input.Module, input.Notification, input.Script)
	return struct{}{}, nil
}

func (r *rpcs) Done(input struct {
	Module       string `rfc7951:"notifyd-v1:module"`
	Notification string `rfc7951:"notifyd-v1:notification"`
	Script       string `rfc7951:"notifyd-v1:script"`
}) (struct{}, error) {
	r.notifier.Done(input.Module, input.Notification, input.Script)
	return struct{}{}, nil
}

func (r *rpcs) ListHandlers(_ struct{}) (*data.Tree, error) {
	stats := r.notifier.Stats()
	return data.TreeNew().
		Assoc("/notifyd-v1:static/handlers",
			convHandlers(stats.StaticHandlers)).
		Assoc("/notifyd-v1:transient/handlers",
			convHandlers(stats.TransientHandlers)).
		Assoc("/notifyd-v1:collapsed/handlers",
			convHandlers(stats.CollapsedHandlers)), nil
}

func convHandlers(in *hashmap.Map) *data.Array {
	out := data.ArrayNew()
	var id uint64
	in.Range(func(key string, v *hashset.Set) {
		keyParts := strings.Split(key, ":")
		if len(keyParts) != 2 {
			return
		}
		module, event := keyParts[0], keyParts[1]
		v.Range(func(script string) {
			out = out.Append(data.ObjectWith(
				data.PairNew("id", id),
				data.PairNew("module", module),
				data.PairNew("notification", event),
				data.PairNew("script", script),
			))
			id++
		})
	})
	return out
}

type state struct {
	notifier *notifyd.Notifier
}

func (s *state) Get() *data.Tree {
	stats := s.notifier.Stats()
	return data.TreeNew().
		Assoc("/notifyd-v1:notifyd/state/registered-handlers",
			stats.NumRegisteredHandlers).
		Assoc("/notifyd-v1:notifyd/state/scripts-executed",
			stats.NumScriptsExecd).
		Assoc("/notifyd-v1:notifyd/state/concurrency-limit",
			stats.NumWorkers)
}

type subscriber struct {
	*vci.Client
}

func (s subscriber) Subscribe(
	module, notif string,
	sub interface{},
) notifyd.Subscription {
	return s.Client.Subscribe(module, notif, sub)
}

func main() {
	comp := vci.NewComponent("net.vyatta.vci.notifyd")
	notifier := notifyd.New(notifyd.SubscribeWith(
		subscriber{comp.Client()},
	))
	comp.Model("net.vyatta.vci.notifyd.v1").
		RPC("notifyd-v1", &rpcs{
			notifier: notifier,
		}).
		State(&state{
			notifier: notifier,
		})
	err := comp.Run()
	if err != nil {
		log.Fatal(err)
	}
	comp.Wait()
}
