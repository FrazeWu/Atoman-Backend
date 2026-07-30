package handlers

import "testing"

func TestTimelineHandlerOnlyOwnsRouteAssemblyAndDateParsing(t *testing.T) {
	functions := topLevelFunctionNames(t, "timeline_handler.go")
	expected := map[string]bool{
		"parseDateTime":       true,
		"SetupTimelineRoutes": true,
	}
	if len(functions) != len(expected) {
		t.Fatalf("timeline_handler.go functions = %v, want exactly %v", functions, expected)
	}
	for name := range expected {
		if !functions[name] {
			t.Fatalf("timeline_handler.go must keep %s", name)
		}
	}
}

func TestTimelineSwaggerAnnotationsStayWithTheirHandlers(t *testing.T) {
	assertSwaggerAnnotations(t, "timeline*_handler.go", []string{
		"GetTimelineEvents",
		"GetTimelineEvent",
		"CreateTimelineEvent",
		"UpdateTimelineEvent",
		"DeleteTimelineEvent",
		"GetTimelinePersons",
		"GetTimelinePerson",
		"GetPersonLocations",
		"CreateTimelinePerson",
		"UpdateTimelinePerson",
		"DeleteTimelinePerson",
		"AddPersonLocation",
		"UpdatePersonLocation",
		"DeletePersonLocation",
		"GetTimelineEventHistory",
		"RevertTimelineEvent",
	})
}
