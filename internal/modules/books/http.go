package books

import (
	"atoman/internal/middleware"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
)

// RegisterRoutes registers private import routes under /api/v1/books.
func RegisterRoutes(group *gin.RouterGroup, service *Service) {
	handler := &Handler{service: service}
	public := group.Group("")
	public.Use(middleware.OptionalAuthMiddleware())
	public.GET("/booklists", handler.searchBookLists)
	public.GET("/booklists/:listId", handler.getBookList)
	public.GET("/asset-revisions/:revisionId/passages", handler.listBookDiscussionPassages)
	public.GET("/passages/:passageId", handler.getBookDiscussionPassage)
	public.GET("/works/:workId/reviews", handler.listBookContentReviews)
	public.GET("/works/:workId/rating", handler.getBookWorkRating)
	public.GET("/rankings/high-rated", handler.listHighRatedBookWorks)
	public.GET("/asset-revisions/:revisionId/content", handler.getBookRevisionContent)
	public.GET("/assets/:assetId/revisions", handler.listBookAssetRevisions)
	public.GET("/works", handler.searchBookWorks)
	public.GET("/works/:workId", handler.getBookWork)
	public.GET("/people", handler.searchBookPeople)
	public.GET("/works/:workId/assets", handler.listBookResources)
	public.GET("/works/:workId/reading-recommendation", handler.recommendBookReading)
	public.GET("/wiki/:entityType/:entityId/revisions", handler.listBookWikiRevisions)
	public.GET("/wiki/:entityType/:entityId", handler.getBookWikiMetadata)
	public.GET("/wiki/:entityType/:entityId/merges", handler.listBookWikiMerges)
	protected := group.Group("")
	protected.Use(requireCurrentUser())
	protected.GET("/me/booklists", handler.listMyBookLists)
	protected.GET("/me/booklists/:listId/draft", handler.getBookListDraft)
	protected.PUT("/me/booklists/:listId/draft", handler.saveBookListDraft)
	protected.POST("/me/booklists/:listId/publish", handler.publishBookList)
	protected.POST("/me/booklists/:listId/unpublish", handler.unpublishBookList)
	protected.DELETE("/me/booklists/:listId", handler.deleteBookList)
	protected.GET("/me/booklist-bookmarks", handler.listBookListBookmarks)
	protected.PUT("/booklists/:listId/bookmark", handler.saveBookListBookmark)
	protected.DELETE("/booklists/:listId/bookmark", handler.deleteBookListBookmark)
	protected.POST("/asset-revisions/:revisionId/passages", handler.createBookDiscussionPassage)
	protected.PUT("/works/:workId/reviews/:contentId", handler.linkBookContentReview)
	protected.DELETE("/works/:workId/reviews/:contentId", handler.unlinkBookContentReview)
	protected.PUT("/works/:workId/rating", handler.setBookWorkRating)
	protected.DELETE("/works/:workId/rating", handler.deleteBookWorkRating)
	protected.POST("/me/reading-sessions/:sessionId/heartbeat", handler.recordBookReadingHeartbeat)
	protected.GET("/me/reading-statistics", handler.getBookReadingStatistics)
	protected.POST("/me/private-notes/:noteId/publish", handler.publishBookPrivateNote)
	protected.GET("/me/library", handler.listBookLibraryEntries)
	protected.PUT("/me/library/works/:workId", handler.saveBookLibraryEntry)
	protected.DELETE("/me/library/entries/:entryId", handler.deleteBookLibraryEntry)
	protected.GET("/me/private-notes", handler.listBookPrivateNotes)
	protected.GET("/me/private-notes/export", handler.exportBookPrivateNotes)
	protected.POST("/me/private-notes/import", handler.importBookPrivateNotes)
	protected.GET("/me/private-notes/:noteId", handler.getBookPrivateNote)
	protected.PUT("/me/private-notes/:noteId", handler.saveBookPrivateNote)
	protected.DELETE("/me/private-notes/:noteId", handler.deleteBookPrivateNote)
	protected.POST("/me/private-notes/:noteId/restore", handler.restoreBookPrivateNote)
	protected.GET("/me/selections/:workId", handler.getBookReadingSelection)
	protected.PUT("/me/selections/:workId", handler.setBookReadingSelection)
	protected.POST("/me/reading-sessions", handler.createBookReadingSession)
	protected.GET("/me/reading-states/:revisionId", handler.getBookCloudReadingState)
	protected.PUT("/me/reading-states/:revisionId", handler.saveBookCloudReadingState)
	protected.GET("/me/reading-marks/:revisionId", handler.listBookRevisionMarks)
	protected.GET("/me/reading-marks", handler.listBookPrivateHighlights)
	protected.PUT("/me/reading-marks/:revisionId/:markId", handler.saveBookRevisionMark)
	protected.DELETE("/me/reading-marks/:revisionId/:markId", handler.deleteBookRevisionMark)
	protected.POST("/works", handler.createBookWork)
	protected.POST("/works/:workId/editions", handler.createBookEdition)
	protected.POST("/people", handler.createBookPerson)
	protected.POST("/works/:workId/assets", handler.createBookResource)
	protected.PATCH("/assets/:assetId/status", handler.setBookResourceStatus)
	protected.POST("/assets/:assetId/upload-sessions", handler.createBookResourceUpload)
	protected.GET("/upload-sessions/:uploadId", handler.getBookResourceUpload)
	protected.POST("/upload-sessions/:uploadId/parts/:partNumber", handler.createBookResourceUploadPart)
	protected.POST("/upload-sessions/:uploadId/parts/:partNumber/complete", handler.completeBookResourceUploadPart)
	protected.POST("/upload-sessions/:uploadId/complete", handler.completeBookResourceUpload)
	protected.PUT("/works/:workId/default-assets/:language", handler.setBookDefaultAsset)
	protected.PATCH("/works/:workId", handler.patchBookWork)
	protected.PATCH("/wiki/:entityType/:entityId", handler.patchBookMetadata)
	protected.POST("/wiki/revisions/:revisionId/revert", handler.revertBookWikiRevision)
	protected.POST("/wiki/merges/preview", handler.previewBookWorkMerge)
	protected.POST("/wiki/merges", handler.mergeBookWorks)
	protected.POST("/wiki/merges/:mergeId/revert", handler.revertBookWorkMerge)
}

type Handler struct {
	service *Service
}

func requireCurrentUser() gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, ok := authctx.Current(c); ok {
			c.Next()
			return
		}
		middleware.AuthMiddleware()(c)
	}
}

func currentBookUser(c *gin.Context) (authctx.CurrentUser, bool) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
	}
	return user, ok
}
