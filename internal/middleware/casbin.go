package middleware

import (
	"log"
	"net/http"
	"strings"

	"github.com/casbin/casbin/v3"
	"github.com/casbin/casbin/v3/model"
	gormadapter "github.com/casbin/gorm-adapter/v3"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var Enforcer *casbin.Enforcer

// InitCasbin initializes the casbin enforcer with GORM adapter
func InitCasbin(db *gorm.DB) error {
	adapter, err := gormadapter.NewAdapterByDB(db)
	if err != nil {
		return err
	}

	// Initialize the casbin model
	m, err := model.NewModelFromString(`
[request_definition]
r = sub, obj, act

[policy_definition]
p = sub, obj, act

[role_definition]
g = _, _

[policy_effect]
e = some(where (p.eft == allow))

[matchers]
m = g(r.sub, p.sub) && keyMatch2(r.obj, p.obj) && regexMatch(r.act, p.act)
`)
	if err != nil {
		return err
	}

	enforcer, err := casbin.NewEnforcer(m, adapter)
	if err != nil {
		return err
	}

	Enforcer = enforcer

	// Load the policies from DB
	err = Enforcer.LoadPolicy()
	if err != nil {
		return err
	}

	// Add default policies if none exist
	initDefaultPolicies()

	return nil
}

func initDefaultPolicies() {
	policyAdded := false
	groupingAdded := false

	// Anonymous policies
	if ok, _ := Enforcer.AddPolicy("anonymous", "/api/v1/auth/*", "POST"); ok {
		policyAdded = true
	}
	if ok, _ := Enforcer.AddPolicy("anonymous", "/api/*", "GET"); ok {
		policyAdded = true
	}
	if ok, _ := Enforcer.AddPolicy("anonymous", "/uploads/*", "(GET|HEAD)"); ok {
		policyAdded = true
	}
	if ok, _ := Enforcer.AddPolicy("anonymous", "/swagger/*", "(GET|HEAD)"); ok {
		policyAdded = true
	}
	if ok, _ := Enforcer.AddPolicy("anonymous", "/ws/*", "GET"); ok {
		policyAdded = true
	}

	// User policies (can do most things, ownership enforced by handlers)
	if ok, _ := Enforcer.AddPolicy("user", "/api/*", "(GET|POST|PUT|PATCH|DELETE)"); ok {
		policyAdded = true
	}
	if ok, _ := Enforcer.AddPolicy("user", "/uploads/*", "(GET|HEAD)"); ok {
		policyAdded = true
	}
	if ok, _ := Enforcer.AddPolicy("user", "/swagger/*", "(GET|HEAD)"); ok {
		policyAdded = true
	}
	if ok, _ := Enforcer.AddPolicy("user", "/ws/*", "GET"); ok {
		policyAdded = true
	}

	// Admin policies
	if ok, _ := Enforcer.AddPolicy("admin", "/api/*", "(GET|POST|PUT|PATCH|DELETE)"); ok {
		policyAdded = true
	}
	if ok, _ := Enforcer.AddPolicy("admin", "/uploads/*", "(GET|HEAD)"); ok {
		policyAdded = true
	}
	if ok, _ := Enforcer.AddPolicy("admin", "/swagger/*", "(GET|HEAD)"); ok {
		policyAdded = true
	}
	if ok, _ := Enforcer.AddPolicy("admin", "/ws/*", "GET"); ok {
		policyAdded = true
	}

	if ok, _ := Enforcer.AddGroupingPolicy("owner", "admin"); ok {
		groupingAdded = true
	}

	if policyAdded {
		if err := Enforcer.SavePolicy(); err != nil {
			log.Printf("Casbin SavePolicy warning: %v", err)
		}
	}
	if groupingAdded && !policyAdded {
		if err := Enforcer.SavePolicy(); err != nil {
			log.Printf("Casbin SavePolicy warning: %v", err)
		}
	}
}

// CasbinMiddleware is the Gin middleware for Casbin authorization
func CasbinMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		role := "anonymous"

		// Extract role from context if set by OptionalAuthMiddleware
		if roleVal, exists := c.Get("role"); exists {
			if r, ok := roleVal.(string); ok && r != "" {
				role = r
			}
		}

		path := c.Request.URL.Path
		method := c.Request.Method

		// Special case: ignore OPTIONS
		if method == "OPTIONS" {
			c.Next()
			return
		}

		// Enforce
		allowed, err := Enforcer.Enforce(role, path, method)
		if err != nil {
			log.Printf("Casbin error: %v", err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Error checking permissions"})
			return
		}

		if !allowed {
			if role == "anonymous" && method != "GET" && !strings.HasPrefix(path, "/api/v1/auth") {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Login required to perform this action"})
				return
			}
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "You do not have permission to access this resource"})
			return
		}

		c.Next()
	}
}
