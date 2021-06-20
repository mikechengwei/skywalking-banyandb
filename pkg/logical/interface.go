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

package logical

import (
	"fmt"

	apiv1 "github.com/apache/skywalking-banyandb/api/fbs/v1"
)

type PlanType uint8

const (
	PlanProjection PlanType = iota
	PlanLimit
	PlanOffset
	PlanScan
	PlanOrderBy
	PlanSelection
)

type UnresolvedPlan interface {
	Type() PlanType
	Analyze(Schema) (Plan, error)
}

type Plan interface {
	fmt.Stringer
	Type() PlanType
	Children() []Plan
	Schema() Schema
	Equal(Plan) bool
}

type Expr interface {
	fmt.Stringer
	FieldType() apiv1.FieldType
	Equal(Expr) bool
}

type ResolvableExpr interface {
	Expr
	Resolve(Plan) error
}

type Optimizer interface {
	Apply(Plan) (Plan, error)
}

var predefinedOptimizers = Optimizers{
	NewProjectionPushDown(),
}

var _ Optimizer = (Optimizers)(nil)

type Optimizers []Optimizer

func (o Optimizers) Apply(plan Plan) (Plan, error) {
	for _, opt := range o {
		var err error
		if plan, err = opt.Apply(plan); err != nil {
			return nil, err
		}
	}
	return plan, nil
}
