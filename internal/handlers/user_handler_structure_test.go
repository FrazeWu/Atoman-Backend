package handlers

import "testing"

func TestUserHandlerOnlyOwnsRouteAssemblyAndOwnerMiddleware(t *testing.T) {
	functions := topLevelFunctionNames(t, "user_handler.go")
	expected := map[string]bool{
		"SetupUserRoutes": true,
		"RequireOwner":    true,
	}
	if len(functions) != len(expected) {
		t.Fatalf("user_handler.go functions = %v, want exactly %v", functions, expected)
	}
	for name := range expected {
		if !functions[name] {
			t.Fatalf("user_handler.go must keep %s", name)
		}
	}
}

func TestUserSwaggerAnnotationsStayWithTheirHandlers(t *testing.T) {
	assertSwaggerAnnotations(t, "user*_handler.go", []string{
		"ChangePassword",
		"GetCurrentUser",
		"GetUserByUsername",
		"GetUserProfile",
		"UpdateUserProfile",
		"GetUserSettings",
		"UpdateUserSettings",
		"FollowUser",
		"UnfollowUser",
		"GetUserFollowers",
		"GetUserFollowing",
		"SearchUsers",
		"ListUsersForRoleManagement",
		"UpdateUserRole",
	})
}
