package feed

func finalizeSubscribedTimeline(items []TimelineItemDTO, query FeedQuery, priorityInboxMode bool, priorities subscriptionPriorityIndex) ([]TimelineItemDTO, int64) {
	items = filterTimeline(items, query)
	if priorityInboxMode {
		applySubscriptionPriority(items, priorities)
		return priorityInbox(items)
	}
	sortTimeline(items)
	return paginateTimeline(items, normalizedPage(query.Page), normalizedPageSize(query.PageSize))
}
