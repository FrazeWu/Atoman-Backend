package feed

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"
)

func TestAutoSubscriptionFilesOwnOneResponsibility(t *testing.T) {
	expected := map[string][]string{
		"auto_subscription_types.go": {
			"Error",
			"newAutoSubscriptionHTTPError",
			"firstNonBlank",
		},
		"auto_subscription_resolver.go": {
			"ResolveSubscriptionInput",
			"resolveSubscriptionInputForUser",
			"parseAutoSubscriptionURL",
			"githubRepositoryTarget",
			"validGithubPathSegment",
			"malformedGithubRepositoryPath",
			"classifyAutoSubscriptionTarget",
			"findExistingAutoSubscriptionSource",
			"findUserSubscriptionForSource",
			"sourceDTOFromTarget",
			"sourceDTOFromModel",
			"autoSubscriptionTargetFromDirectFeedURL",
			"probeAutoSubscriptionDirectFeedURL",
			"isAutoSubscriptionFeedContentType",
			"looksLikeAutoSubscriptionFeedDocument",
			"fetchAutoSubscriptionDocument",
			"resolveDiscoveredSubscriptionInput",
			"resolveDiscoveredSubscriptionHTML",
			"newAutoSubscriptionResolveResponse",
			"messageForAutoSubscriptionStatus",
		},
		"auto_subscription_creator.go": {
			"AutoAddSubscription",
			"autoSubscriptionTargetForAdd",
			"createAutoSubscription",
			"findOrCreateAutoAddFeedSource",
			"findReusableAutoAddFeedSource",
			"updateAutoAddFeedSource",
			"uniqueNonBlankStrings",
			"isAutoSubscriptionDuplicateSubscriptionError",
			"autoSubscriptionGroup",
			"autoSubscriptionTargetFromSource",
			"validAutoSubscriptionSiteURL",
			"writeAutoSubscriptionError",
		},
	}

	for filename, expectedFunctions := range expected {
		parsed, err := parser.ParseFile(token.NewFileSet(), filename, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", filename, err)
		}

		actual := make(map[string]bool)
		for _, declaration := range parsed.Decls {
			if function, ok := declaration.(*ast.FuncDecl); ok {
				actual[function.Name.Name] = true
			}
		}
		if len(actual) != len(expectedFunctions) {
			t.Fatalf("%s functions = %v, want exactly %v", filename, actual, expectedFunctions)
		}
		for _, name := range expectedFunctions {
			if !actual[name] {
				t.Fatalf("%s is missing %s", filename, name)
			}
		}
	}

	if _, err := os.Stat("auto_subscription.go"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("auto_subscription.go should be removed after responsibilities are split, stat error = %v", err)
	}
}
