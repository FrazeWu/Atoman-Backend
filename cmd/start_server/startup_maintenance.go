package main

import (
	"log"

	"gorm.io/gorm"
)

func runStartupMaintenance(db *gorm.DB) error {
	db.Exec(`INSERT INTO site_settings (key, value, description, updated_at)
VALUES ('forum.solved_auto_threshold', '10', '回复点赞数达到该值时自动标记为解决方案', NOW())
ON CONFLICT (key) DO NOTHING`)
	db.Exec(`INSERT INTO site_settings (key, value, description, updated_at)
VALUES ('site.module_access', '{"modules":{"feed":{"visible":true,"features":{"subscription.manage":true}},"music":{"visible":true,"features":{"music.submit":true,"music.review":true}},"blog":{"visible":true,"features":{"post.create":true,"channel.manage":true}},"forum":{"visible":true,"features":{"topic.create":true,"category.request":true}},"debate":{"visible":true,"features":{"debate.create":true,"debate.edit":true}},"timeline":{"visible":true,"features":{"timeline.edit":true}},"podcast":{"visible":true,"features":{"podcast.publish":true}},"video":{"visible":true,"features":{"video.publish":true}}}}', '模块可见性与功能开放配置', NOW())
ON CONFLICT (key) DO NOTHING`)

	log.Println("Running blog channel field backfill...")
	backfillBlogChannelFields(db)
	log.Println("Running external RSS full text enablement backfill...")
	backfillExternalRSSFullTextEnabled(db)
	log.Println("Running internal RSS feed source backfill...")
	backfillInternalRSSFeedSources(db)
	ensureSoftDeleteColumns(db)
	return bootstrapOwnerFromEnv(db)
}
