/*
Copyright 2023 The Karmada Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package thirdparty

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/json"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"

	configv1alpha1 "github.com/karmada-io/karmada/pkg/apis/config/v1alpha1"
	workv1alpha2 "github.com/karmada-io/karmada/pkg/apis/work/v1alpha2"
	"github.com/karmada-io/karmada/pkg/resourceinterpreter/customized/declarative"
	"github.com/karmada-io/karmada/pkg/resourceinterpreter/customized/declarative/luavm"
	"github.com/karmada-io/karmada/pkg/util/interpreter"
)

var rules interpreter.Rules = interpreter.AllResourceInterpreterCustomizationRules

func checkScript(script string) error {
	ctx, cancel := context.WithTimeout(context.TODO(), time.Second)
	defer cancel()
	l, err := luavm.NewWithContext(ctx)
	if err != nil {
		return err
	}
	defer l.Close()
	_, err = l.LoadString(script)
	return err
}

// TestCase represents a complete test case in a single file
type TestCase struct {
	// Test metadata
	Name        string `yaml:"name"`
	Description string `yaml:"description,omitempty"`
	Operation   string `yaml:"operation"`

	// Input data (inline)
	DesiredInput   map[string]interface{}   `yaml:"desiredInput,omitempty"`
	ObservedInput  map[string]interface{}   `yaml:"observedInput,omitempty"`
	StatusInput    []map[string]interface{} `yaml:"statusInput,omitempty"`
	DesiredReplica int64                    `yaml:"desiredReplica,omitempty"`

	// Expected output (inline)
	ExpectedOutput *ExpectedOutput `yaml:"expectedOutput,omitempty"`
}

type ExpectedOutput struct {
	// For AggregateStatus operation
	AggregatedStatus map[string]interface{} `yaml:"aggregatedStatus,omitempty"`
	// For InterpretDependency operation
	Dependencies []map[string]interface{} `yaml:"dependencies,omitempty"`
	// For Retain operation
	Retained map[string]interface{} `yaml:"retained,omitempty"`
	// For InterpretReplica operation (returns replica and requires)
	Replica  *int32                 `yaml:"replica,omitempty"`
	Requires map[string]interface{} `yaml:"requires,omitempty"`
	// For ReviseReplica operation
	Revised map[string]interface{} `yaml:"revised,omitempty"`
	// For InterpretStatus operation
	Status map[string]interface{} `yaml:"status,omitempty"`
	// For InterpretHealth operation
	Healthy *bool `yaml:"healthy,omitempty"`
	// For InterpretComponent operation
	Components []map[string]interface{} `yaml:"components,omitempty"`
}

func checkInterpretationRule(t *testing.T, path string, configs []*configv1alpha1.ResourceInterpreterCustomization) {
	ipt := declarative.NewConfigurableInterpreter(nil)
	ipt.LoadConfig(configs)

	dir := filepath.Dir(path)
	testsDir := filepath.Join(dir, "tests")

	// Check if tests directory exists
	if _, err := os.Stat(testsDir); os.IsNotExist(err) {
		t.Logf("No tests directory found at %s, skipping tests", testsDir)
		return
	}

	// Read all test case files from tests directory
	testFiles, err := filepath.Glob(filepath.Join(testsDir, "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	if len(testFiles) == 0 {
		t.Logf("No test files found in %s", testsDir)
		return
	}

	for _, testFile := range testFiles {
		// Read test case file
		data, err := os.ReadFile(testFile)
		if err != nil {
			t.Fatalf("failed to read test file %s: %v", testFile, err)
		}

		var testCase TestCase
		if err := k8syaml.Unmarshal(data, &testCase); err != nil {
			t.Fatalf("failed to unmarshal test file %s: %v", testFile, err)
		}

		// Run test for each customization
		for _, customization := range configs {
			testName := fmt.Sprintf("%s/%s", customization.Name, testCase.Name)
			t.Run(testName, func(t *testing.T) {
				rule := rules.GetByOperation(testCase.Operation)
				if rule == nil {
					t.Fatalf("operation %s is not supported. Use one of: %s", testCase.Operation, strings.Join(rules.Names(), ", "))
				}

				// Check script syntax
				if err := checkScript(rule.GetScript(customization)); err != nil {
					t.Fatalf("checking %s of %s, expected nil, but got: %v", rule.Name(), customization.Name, err)
				}

				// Prepare arguments
				args := interpreter.RuleArgs{Replica: testCase.DesiredReplica}
				if testCase.DesiredInput != nil {
					args.Desired = &unstructured.Unstructured{Object: testCase.DesiredInput}
				}
				if testCase.ObservedInput != nil {
					args.Observed = &unstructured.Unstructured{Object: testCase.ObservedInput}
				}
				if testCase.StatusInput != nil {
					args.Status = convertToAggregatedStatusItems(t, testCase.StatusInput)
				}

				// Execute rule
				result := rule.Run(ipt, args)

				// Verify result
				verifyRuleResult(t, customization.Name, rule.Name(), result, testCase.ExpectedOutput)
			})
		}
	}
}

// convertToAggregatedStatusItems converts map slice to AggregatedStatusItem slice
func convertToAggregatedStatusItems(t *testing.T, items []map[string]interface{}) []workv1alpha2.AggregatedStatusItem {
	if items == nil {
		return nil
	}

	result := make([]workv1alpha2.AggregatedStatusItem, 0, len(items))
	for _, item := range items {
		data, err := json.Marshal(item)
		if err != nil {
			t.Fatalf("failed to marshal status item: %v", err)
		}

		var statusItem workv1alpha2.AggregatedStatusItem
		if err := json.Unmarshal(data, &statusItem); err != nil {
			t.Fatalf("failed to unmarshal status item: %v", err)
		}
		result = append(result, statusItem)
	}
	return result
}

// verifyRuleResult verifies that the rule execution result matches the expected output.
//
// Verification Strategy:
//   - If expected is nil: Skip verification entirely (allows tests without expected outputs)
//   - If expected is not nil but a specific field is nil: Skip that field's verification (flexible testing)
//   - If expected field is defined: Strictly verify and fail if mismatch
func verifyRuleResult(t *testing.T, customizationName string, operation string, result *interpreter.RuleResult, expected *ExpectedOutput) {
	if expected == nil {
		t.Fatalf("missing expected output for %s", customizationName)
		return
	}

	if result.Err != nil {
		t.Fatalf("execute %s %s error: %v", customizationName, operation, result.Err)
		return
	}

	if len(result.Results) == 0 {
		t.Fatalf("execute %s %s returned no results", customizationName, operation)
		return
	}

	// Build a map of actual results by name for easy lookup
	actualResults := make(map[string]interface{})
	for _, res := range result.Results {
		actualResults[res.Name] = res.Value
	}

	// Verify each result based on its name
	for resultName, resultValue := range actualResults {
		verifyResultByName(t, customizationName, resultName, resultValue, expected)
	}
}

// verifyResultByName verifies a single result based on its name
func verifyResultByName(t *testing.T, customizationName string, resultName string, resultValue interface{}, expected *ExpectedOutput) {
	switch resultName {
	case "aggregatedStatus":
		verifyOptionalJSONField(t, customizationName, "AggregateStatus", resultValue, expected.AggregatedStatus)
	case "dependencies":
		verifyOptionalJSONField(t, customizationName, "InterpretDependency", resultValue, expected.Dependencies)
	case "retained":
		verifyOptionalJSONField(t, customizationName, "Retain", resultValue, expected.Retained)
	case "replica":
		verifyReplicaField(t, customizationName, resultValue, expected.Replica)
	case "requires":
		verifyOptionalJSONField(t, customizationName, "InterpretReplica 'requires'", resultValue, expected.Requires)
	case "revised":
		verifyOptionalJSONField(t, customizationName, "ReviseReplica", resultValue, expected.Revised)
	case "status":
		verifyOptionalJSONField(t, customizationName, "InterpretStatus", resultValue, expected.Status)
	case "healthy":
		verifyHealthyField(t, customizationName, resultValue, expected.Healthy)
	case "components":
		verifyOptionalJSONField(t, customizationName, "InterpretComponent", resultValue, expected.Components)
	default:
		t.Logf("Unknown result name: %s, skipping verification", resultName)
	}
}

// verifyOptionalJSONField verifies a field if the expected value is provided, otherwise skips
func verifyOptionalJSONField(t *testing.T, customizationName string, operation string, actual interface{}, expected interface{}) {
	if expected == nil {
		t.Fatalf("missing expected '%s' output for %s", operation, customizationName)
		return
	}
	verifyJSONMatch(t, customizationName, operation, actual, expected)
}

// verifyReplicaField verifies the replica field (int32)
func verifyReplicaField(t *testing.T, customizationName string, resultValue interface{}, expectedReplica *int32) {
	if expectedReplica == nil {
		t.Fatalf("missing expected 'replica' for %s", customizationName)
		return
	}

	actualReplica, ok := resultValue.(int32)
	if !ok {
		// Try to convert from float64 (JSON unmarshal default for numbers)
		if f, ok := resultValue.(float64); ok {
			actualReplica = int32(f)
		} else {
			t.Fatalf("InterpretReplica 'replica' result is not int32 for %s, got %T", customizationName, resultValue)
		}
	}

	if actualReplica != *expectedReplica {
		t.Fatalf("InterpretReplica 'replica' result mismatch for %s: expected %d, got %d",
			customizationName, *expectedReplica, actualReplica)
	}
	t.Logf("✓ InterpretReplica 'replica' verification passed for %s (value: %d)", customizationName, actualReplica)
}

// verifyHealthyField verifies the healthy field (bool)
func verifyHealthyField(t *testing.T, customizationName string, resultValue interface{}, expectedHealthy *bool) {
	if expectedHealthy == nil {
		t.Fatalf("missing expected 'healthy' for %s", customizationName)
		return
	}

	actualHealthy, ok := resultValue.(bool)
	if !ok {
		t.Fatalf("InterpretHealth result is not bool for %s, got %T", customizationName, resultValue)
	}

	if actualHealthy != *expectedHealthy {
		t.Fatalf("InterpretHealth result mismatch for %s: expected %v, got %v",
			customizationName, *expectedHealthy, actualHealthy)
	}
	t.Logf("✓ InterpretHealth verification passed for %s (value: %v)", customizationName, actualHealthy)
}

// verifyJSONMatch compares actual and expected values via JSON marshaling
func verifyJSONMatch(t *testing.T, customizationName string, operation string, actual interface{}, expected interface{}) {
	actualJSON, err := json.Marshal(actual)
	if err != nil {
		t.Fatalf("failed to marshal actual result for %s: %v", operation, err)
	}

	var actualData interface{}
	if err := json.Unmarshal(actualJSON, &actualData); err != nil {
		t.Fatalf("failed to unmarshal actual result for %s: %v", operation, err)
	}

	expectedJSON, err := json.Marshal(expected)
	if err != nil {
		t.Fatalf("failed to marshal expected result for %s: %v", operation, err)
	}
	var expectedData interface{}
	if err := json.Unmarshal(expectedJSON, &expectedData); err != nil {
		t.Fatalf("failed to unmarshal expected result for %s: %v", operation, err)
	}

	if !compareJSON(actualData, expectedData) {
		t.Fatalf("%s result mismatch for %s\nExpected:\n%s\nActual:\n%s",
			operation, customizationName, prettyJSON(expectedData), prettyJSON(actualData))
	}
	t.Logf("✓ %s verification passed for %s", operation, customizationName)
}

func compareJSON(a, b interface{}) bool {
	aJSON, err := json.Marshal(a)
	if err != nil {
		return false
	}
	bJSON, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return string(aJSON) == string(bJSON)
}

func prettyJSON(v interface{}) string {
	data, err := yaml.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(data)
}

func TestThirdPartyCustomizationsFile(t *testing.T) {
	err := filepath.Walk("resourcecustomizations", func(path string, f os.FileInfo, err error) error {
		if err != nil {
			// cannot happen
			return err
		}
		if f.IsDir() {
			return nil
		}
		if strings.Contains(path, "testdata") {
			return nil
		}
		if filepath.Base(path) != configurableInterpreterFile {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			// cannot happen
			return err
		}
		var configs []*configv1alpha1.ResourceInterpreterCustomization
		decoder := k8syaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
		for {
			config := &configv1alpha1.ResourceInterpreterCustomization{}
			err = decoder.Decode(config)
			if err != nil {
				break
			}
			dirSplit := strings.Split(path, string(os.PathSeparator))
			if len(dirSplit) != 5 {
				return fmt.Errorf("the directory format is incorrect. Dir: %s", path)
			}
			if config.Spec.Target.APIVersion != fmt.Sprintf("%s/%s", dirSplit[1], dirSplit[2]) {
				return fmt.Errorf("Target.APIVersion does not match directory format. Target.APIVersion: %s, Dir: %s", config.Spec.Target.APIVersion, path)
			}
			if config.Spec.Target.Kind != dirSplit[3] {
				return fmt.Errorf("Target.Kind does not match directory format. Target.Kind: %s, Dir: %s", config.Spec.Target.Kind, path)
			}
			configs = append(configs, config)
		}
		if err != io.EOF {
			return err
		}
		checkInterpretationRule(t, path, configs)
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil, but got: %v", err)
	}
}
