package collab

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func signedUserHubTokenForTest(t *testing.T, jwtSecret string, userID uuid.UUID) string {
	t.Helper()

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user_id": userID.String(),
		"exp":     time.Now().Add(time.Hour).Unix(),
	})
	signed, err := token.SignedString([]byte(jwtSecret))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return signed
}

func userHubGinContextForTest(req *http.Request) *gin.Context {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	return c
}

func TestExtractUserIDFromRequestAcceptsSharedAuthCookie(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := "test-secret"
	userID := uuid.New()
	token := signedUserHubTokenForTest(t, secret, userID)
	req := httptest.NewRequest(http.MethodGet, "/ws/user", nil)
	req.AddCookie(&http.Cookie{Name: "atoman_token", Value: token})

	got, err := extractUserIDFromRequest(userHubGinContextForTest(req), secret)

	if err != nil {
		t.Fatalf("expected cookie token to be accepted, got error: %v", err)
	}
	if got != userID {
		t.Fatalf("expected user ID %s, got %s", userID, got)
	}
}

func TestExtractUserIDFromRequestKeepsBearerAndQueryCompatibility(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := "test-secret"
	headerUserID := uuid.New()
	queryUserID := uuid.New()
	headerToken := signedUserHubTokenForTest(t, secret, headerUserID)
	queryToken := signedUserHubTokenForTest(t, secret, queryUserID)

	headerReq := httptest.NewRequest(http.MethodGet, "/ws/user", nil)
	headerReq.Header.Set("Authorization", "Bearer "+headerToken)
	headerGot, err := extractUserIDFromRequest(userHubGinContextForTest(headerReq), secret)
	if err != nil {
		t.Fatalf("expected bearer token to be accepted, got error: %v", err)
	}
	if headerGot != headerUserID {
		t.Fatalf("expected bearer user ID %s, got %s", headerUserID, headerGot)
	}

	queryReq := httptest.NewRequest(http.MethodGet, "/ws/user?token="+queryToken, nil)
	queryGot, err := extractUserIDFromRequest(userHubGinContextForTest(queryReq), secret)
	if err != nil {
		t.Fatalf("expected query token to be accepted, got error: %v", err)
	}
	if queryGot != queryUserID {
		t.Fatalf("expected query user ID %s, got %s", queryUserID, queryGot)
	}
}
