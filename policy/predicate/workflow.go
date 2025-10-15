// Copyright 2018 Palantir Technologies, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package predicate

import (
	"context"
	"fmt"
	"strings"

	"github.com/palantir/policy-bot/policy/common"
	"github.com/palantir/policy-bot/pull"
)

type HasWorkflowResult struct {
	Conclusions AllowedConclusions `yaml:"conclusions,omitempty"`
	Workflows   []string           `yaml:"workflows,omitempty"`
}

func NewHasWorkflowResult(workflows []string, conclusions []string) *HasWorkflowResult {
	return &HasWorkflowResult{
		Conclusions: conclusions,
		Workflows:   workflows,
	}
}

var _ Predicate = HasWorkflowResult{}

func (pred HasWorkflowResult) Evaluate(ctx context.Context, prctx pull.Context) (*common.PredicateResult, error) {
	// TODO: Implement workflow run checking
	// This is a stub implementation - workflow results are not yet supported

	allowedConclusions := pred.Conclusions
	if len(allowedConclusions) == 0 {
		allowedConclusions = AllowedConclusions{"success"}
	}

	// For now, return not satisfied with a message that workflow checks are not implemented
	predicateResult := common.PredicateResult{
		ValuePhrase:     "workflow results",
		ConditionPhrase: fmt.Sprintf("exist and have conclusion %s", allowedConclusions.joinWithOr()),
		Description:     "Workflow run checking not yet implemented - missing workflows: " + strings.Join(pred.Workflows, ", "),
		Values:          pred.Workflows,
		Satisfied:       false,
	}

	return &predicateResult, nil
}

func (pred HasWorkflowResult) Trigger() common.Trigger {
	return common.TriggerStatus
}
