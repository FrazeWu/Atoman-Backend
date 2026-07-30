package service

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestOAuthServiceMethodsAreSplitByFlowStage(t *testing.T) {
	expected := map[string][]string{
		"oauth_service.go": {
			"NewOAuthService",
			"ProviderNames",
			"CreateWebSession",
			"Begin",
		},
		"oauth_callback_service.go": {
			"HandleCallback",
		},
		"oauth_completion_service.go": {
			"CompleteProfile",
			"SetPassword",
			"ConfirmAccount",
		},
		"oauth_pending_service.go": {
			"ListIdentities",
			"SendPendingEmailVerification",
			"VerifyPendingEmail",
			"PendingInfo",
			"CancelPending",
			"Unlink",
			"pendingFlow",
		},
		"oauth_helpers.go": {
			"randomOAuthToken",
			"hashOAuthSecret",
			"validateOAuthPassword",
			"oauthCodeChallenge",
			"sanitizeOAuthReturnTo",
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
}
