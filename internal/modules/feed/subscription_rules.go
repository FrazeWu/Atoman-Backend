package feed

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/model"
)

type subscriptionRuleInput struct {
	Name                     string          `json:"name"`
	Enabled                  bool            `json:"enabled"`
	MatchType                string          `json:"match_type"`
	ConditionsJSON           json.RawMessage `json:"conditions_json"`
	ActionGroupID            *uuid.UUID      `json:"action_group_id"`
	ActionMuted              *bool           `json:"action_muted"`
	ActionAutoMarkRead       *bool           `json:"action_auto_mark_read"`
	ActionAutoAddReadingList *bool           `json:"action_auto_add_reading_list"`
}

func RegisterSubscriptionRuleRoutes(group *gin.RouterGroup, db *gorm.DB) {
	group.GET("/subscription-rules", listSubscriptionRules(db))
	group.POST("/subscription-rules", createSubscriptionRule(db))
	group.PUT("/subscription-rules/:id", updateSubscriptionRule(db))
	group.DELETE("/subscription-rules/:id", deleteSubscriptionRule(db))
	group.PUT("/subscription-rules/reorder", reorderSubscriptionRules(db))
	group.POST("/subscription-rules/apply", applySubscriptionRules(db))
}

func subscriptionRuleUser(c *gin.Context) (uuid.UUID, bool) {
	user, ok := c.Get("user_id")
	id, valid := user.(uuid.UUID)
	return id, ok && valid
}

func ruleInput(c *gin.Context) (subscriptionRuleInput, bool) {
	var input subscriptionRuleInput
	if c.ShouldBindJSON(&input) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return input, false
	}
	input.Name = strings.TrimSpace(input.Name)
	var conditions map[string][]string
	if input.Name == "" || json.Unmarshal(input.ConditionsJSON, &conditions) != nil || len(conditions[map[string]string{"source_category": "categories", "source_ids": "source_ids", "keywords": "keywords"}[input.MatchType]]) == 0 || (input.ActionGroupID == nil && input.ActionMuted == nil && input.ActionAutoMarkRead == nil && input.ActionAutoAddReadingList == nil) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid subscription rule"})
		return input, false
	}
	return input, true
}

func createSubscriptionRule(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := subscriptionRuleUser(c)
		if !ok {
			c.JSON(401, gin.H{"error": "unauthorized"})
			return
		}
		input, ok := ruleInput(c)
		if !ok {
			return
		}
		var position int
		db.Model(&model.FeedSubscriptionRule{}).Where("user_id = ?", userID).Select("COALESCE(MAX(position), 0)").Scan(&position)
		rule := model.FeedSubscriptionRule{UserID: userID, Name: input.Name, Enabled: input.Enabled, Position: position + 1, MatchType: input.MatchType, ConditionsJSON: input.ConditionsJSON, ActionGroupID: input.ActionGroupID, ActionMuted: input.ActionMuted, ActionAutoMarkRead: input.ActionAutoMarkRead, ActionAutoAddReadingList: input.ActionAutoAddReadingList}
		if err := db.Create(&rule).Error; err != nil {
			c.JSON(500, gin.H{"error": "create subscription rule failed"})
			return
		}
		c.JSON(201, gin.H{"data": rule})
	}
}

func listSubscriptionRules(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := subscriptionRuleUser(c)
		if !ok {
			c.JSON(401, gin.H{"error": "unauthorized"})
			return
		}
		var rules []model.FeedSubscriptionRule
		if err := db.Where("user_id = ?", userID).Order("position ASC").Find(&rules).Error; err != nil {
			c.JSON(500, gin.H{"error": "list subscription rules failed"})
			return
		}
		c.JSON(200, gin.H{"data": rules})
	}
}

func updateSubscriptionRule(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := subscriptionRuleUser(c)
		if !ok {
			c.JSON(401, gin.H{"error": "unauthorized"})
			return
		}
		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(400, gin.H{"error": "invalid rule id"})
			return
		}
		input, ok := ruleInput(c)
		if !ok {
			return
		}
		updates := map[string]any{"name": input.Name, "enabled": input.Enabled, "match_type": input.MatchType, "conditions_json": input.ConditionsJSON, "action_group_id": input.ActionGroupID, "action_muted": input.ActionMuted, "action_auto_mark_read": input.ActionAutoMarkRead, "action_auto_add_reading_list": input.ActionAutoAddReadingList}
		result := db.Model(&model.FeedSubscriptionRule{}).Where("id = ? AND user_id = ?", id, userID).Updates(updates)
		if result.Error != nil {
			c.JSON(500, gin.H{"error": "update subscription rule failed"})
			return
		}
		if result.RowsAffected == 0 {
			c.JSON(404, gin.H{"error": "subscription rule not found"})
			return
		}
		var rule model.FeedSubscriptionRule
		db.First(&rule, "id = ?", id)
		c.JSON(200, gin.H{"data": rule})
	}
}

func deleteSubscriptionRule(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := subscriptionRuleUser(c)
		if !ok {
			c.JSON(401, gin.H{"error": "unauthorized"})
			return
		}
		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(400, gin.H{"error": "invalid rule id"})
			return
		}
		result := db.Where("id = ? AND user_id = ?", id, userID).Delete(&model.FeedSubscriptionRule{})
		if result.Error != nil {
			c.JSON(500, gin.H{"error": "delete subscription rule failed"})
			return
		}
		if result.RowsAffected == 0 {
			c.JSON(404, gin.H{"error": "subscription rule not found"})
			return
		}
		c.JSON(200, gin.H{"message": "ok"})
	}
}

func reorderSubscriptionRules(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := subscriptionRuleUser(c)
		if !ok {
			c.JSON(401, gin.H{"error": "unauthorized"})
			return
		}
		var input struct {
			RuleIDs []uuid.UUID `json:"rule_ids"`
		}
		if c.ShouldBindJSON(&input) != nil {
			c.JSON(400, gin.H{"error": "invalid request"})
			return
		}
		var count int64
		db.Model(&model.FeedSubscriptionRule{}).Where("user_id = ?", userID).Count(&count)
		if int64(len(input.RuleIDs)) != count {
			c.JSON(400, gin.H{"error": "invalid rule order"})
			return
		}
		for i, id := range input.RuleIDs {
			if db.Model(&model.FeedSubscriptionRule{}).Where("id = ? AND user_id = ?", id, userID).Update("position", i+1).RowsAffected == 0 {
				c.JSON(400, gin.H{"error": "invalid rule order"})
				return
			}
		}
		c.JSON(200, gin.H{"message": "ok"})
	}
}

func applySubscriptionRules(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := subscriptionRuleUser(c)
		if !ok {
			c.JSON(401, gin.H{"error": "unauthorized"})
			return
		}
		var input struct {
			RuleID string `json:"rule_id"`
			All    bool   `json:"all"`
		}
		if c.ShouldBindJSON(&input) != nil || (!input.All && input.RuleID == "") {
			c.JSON(400, gin.H{"error": "invalid request"})
			return
		}
		query := db.Where("user_id = ? AND enabled = ?", userID, true).Order("position ASC")
		if !input.All {
			id, err := uuid.Parse(input.RuleID)
			if err != nil {
				c.JSON(400, gin.H{"error": "invalid rule id"})
				return
			}
			query = query.Where("id = ?", id)
		}
		var rules []model.FeedSubscriptionRule
		if err := query.Find(&rules).Error; err != nil {
			c.JSON(500, gin.H{"error": "list subscription rules failed"})
			return
		}
		var subs []model.Subscription
		if err := db.Preload("FeedSource").Where("user_id = ?", userID).Find(&subs).Error; err != nil {
			c.JSON(500, gin.H{"error": "list subscriptions failed"})
			return
		}
		summary := map[string]int{"scanned_count": len(subs), "updated_count": 0, "group_changed_count": 0, "muted_changed_count": 0, "auto_mark_read_changed_count": 0, "auto_add_reading_list_changed_count": 0}
		for _, sub := range subs {
			updates := map[string]any{}
			for _, rule := range rules {
				if !matchesRule(rule, sub) {
					continue
				}
				if rule.ActionGroupID != nil && (sub.SubscriptionGroupID == nil || *sub.SubscriptionGroupID != *rule.ActionGroupID) {
					updates["subscription_group_id"] = *rule.ActionGroupID
					summary["group_changed_count"]++
				}
				if rule.ActionMuted != nil && sub.IsMuted != *rule.ActionMuted {
					updates["is_muted"] = *rule.ActionMuted
					sub.IsMuted = *rule.ActionMuted
					summary["muted_changed_count"]++
				}
				if rule.ActionAutoMarkRead != nil && sub.AutoMarkRead != *rule.ActionAutoMarkRead {
					updates["auto_mark_read"] = *rule.ActionAutoMarkRead
					sub.AutoMarkRead = *rule.ActionAutoMarkRead
					summary["auto_mark_read_changed_count"]++
				}
				if rule.ActionAutoAddReadingList != nil && sub.AutoAddReadingList != *rule.ActionAutoAddReadingList {
					updates["auto_add_reading_list"] = *rule.ActionAutoAddReadingList
					sub.AutoAddReadingList = *rule.ActionAutoAddReadingList
					summary["auto_add_reading_list_changed_count"]++
				}
			}
			if len(updates) > 0 {
				db.Model(&model.Subscription{}).Where("id = ?", sub.ID).Updates(updates)
				summary["updated_count"]++
			}
		}
		c.JSON(200, gin.H{"data": summary})
	}
}

// applySubscriptionRulesForUser applies enabled rules after a new subscription is created.
func applySubscriptionRulesForUser(db *gorm.DB, userID uuid.UUID) {
	if !db.Migrator().HasTable(&model.FeedSubscriptionRule{}) {
		return
	}
	var rules []model.FeedSubscriptionRule
	if db.Where("user_id = ? AND enabled = ?", userID, true).Order("position ASC").Find(&rules).Error != nil || len(rules) == 0 {
		return
	}
	var subscriptions []model.Subscription
	if db.Preload("FeedSource").Where("user_id = ?", userID).Find(&subscriptions).Error != nil {
		return
	}
	applyRulesToSubscriptions(db, rules, subscriptions)
}

func applyRulesToSubscriptions(db *gorm.DB, rules []model.FeedSubscriptionRule, subscriptions []model.Subscription) {
	for _, sub := range subscriptions {
		updates := map[string]any{}
		for _, rule := range rules {
			if !matchesRule(rule, sub) {
				continue
			}
			if rule.ActionGroupID != nil {
				updates["subscription_group_id"] = *rule.ActionGroupID
			}
			if rule.ActionMuted != nil {
				updates["is_muted"] = *rule.ActionMuted
			}
			if rule.ActionAutoMarkRead != nil {
				updates["auto_mark_read"] = *rule.ActionAutoMarkRead
			}
			if rule.ActionAutoAddReadingList != nil {
				updates["auto_add_reading_list"] = *rule.ActionAutoAddReadingList
			}
		}
		if len(updates) > 0 {
			db.Model(&model.Subscription{}).Where("id = ?", sub.ID).Updates(updates)
		}
	}
}

func matchesRule(rule model.FeedSubscriptionRule, sub model.Subscription) bool {
	var conditions map[string][]string
	if json.Unmarshal(rule.ConditionsJSON, &conditions) != nil {
		return false
	}
	if rule.MatchType == "source_ids" {
		return includes(conditions["source_ids"], sub.FeedSourceID.String())
	}
	if sub.FeedSource == nil {
		return false
	}
	if rule.MatchType == "source_category" {
		return includes(conditions["categories"], sub.FeedSource.Category)
	}
	for _, keyword := range conditions["keywords"] {
		if strings.Contains(strings.ToLower(sub.Title+" "+sub.FeedSource.Title), strings.ToLower(strings.TrimSpace(keyword))) {
			return true
		}
	}
	return false
}
func includes(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}
