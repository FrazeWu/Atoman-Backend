package comment

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestCommentServiceMethodsAreSplitByResponsibility(t *testing.T) {
	expected := map[string][]string{
		"service.go": {
			"SetForumPolicy",
			"NewService",
			"createTransactionMutex",
			"withCreateTransactionMutex",
		},
		"service_create.go": {
			"Create",
			"CreateWithExtension",
			"replaceContentReferences",
			"validateCreateTargetTx",
			"checkCreateAbuse",
			"createCommentRelations",
			"ContentHash",
		},
		"service_query.go": {
			"List",
			"Get",
			"targetSummary",
			"ListReplies",
			"safePagination",
			"loadCommentDTO",
			"previewChildren",
			"entryDTOs",
		},
		"service_validation.go": {
			"validateAuthor",
			"resolveVisible",
			"resolveStoredTarget",
			"validateCommentContent",
			"validateAttachments",
			"validateMentions",
			"sortedUUIDs",
			"isMediaTarget",
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
