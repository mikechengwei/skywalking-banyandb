// Licensed to Apache Software Foundation (ASF) under one or more contributor
// license agreements. See the NOTICE file distributed with
// this work for additional information regarding copyright
// ownership. Apache Software Foundation (ASF) licenses this file to you under
// the Apache License, Version 2.0 (the "License"); you may
// not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

package sources

import (
	"context"
	"reflect"

	"github.com/pkg/errors"

	"github.com/apache/skywalking-banyandb/pkg/flow"
)

var _ flow.Source = (*sourceChan)(nil)

type sourceChan struct {
	flow.ComponentState
	in  interface{}
	out chan flow.StreamRecord
}

func (s *sourceChan) Out() <-chan flow.StreamRecord {
	return s.out
}

func (s *sourceChan) Setup(ctx context.Context) error {
	chanVal := reflect.ValueOf(s.in)

	if !chanVal.IsValid() {
		return errors.New("invalid channel")
	}

	s.Add(1)
	go s.run(ctx, chanVal)
	return nil
}

func (s *sourceChan) run(ctx context.Context, chanVal reflect.Value) {
	ctx, cancel := context.WithCancel(ctx)
	defer func() {
		cancel()
		close(s.out)
		s.Done()
	}()

	for {
		val, open := chanVal.Recv()
		if !open {
			return
		}
		select {
		case s.out <- flow.TryExactTimestamp(val.Interface()):
		case <-ctx.Done():
			return
		}
	}
}

func (s *sourceChan) Teardown(ctx context.Context) error {
	s.Wait()
	return nil
}

func (s *sourceChan) Exec(downstream flow.Inlet) {
	s.Add(1)
	go flow.Transmit(&s.ComponentState, downstream, s)
}

func NewChannel(in interface{}) (flow.Source, error) {
	if reflect.TypeOf(in).Kind() != reflect.Chan {
		return nil, errors.New("in must be a Channel")
	}

	return &sourceChan{
		in:  in,
		out: make(chan flow.StreamRecord, 1024),
	}, nil
}
