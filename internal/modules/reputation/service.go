package reputation

import (
	"context"
	"errors"
	"math"
	"sort"
	"time"

	"atoman/internal/model"
	"atoman/internal/modules/blog"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	InitialQuality            = 20.0
	BlogPriorMean             = 50.0
	BlogPriorStrength         = 5.0
	BlogMinimumRatingCount    = 6
	QualityDecay              = 0.5
	QualityAlgorithmVersionV1 = "blog-quality-v1"
	WeightAlgorithmVersionV1  = "linear-quality-v1"
)

type Service struct{ db *gorm.DB }

func NewService(db *gorm.DB) *Service { return &Service{db: db} }

type UserSnapshotDTO struct {
	Quality             float64   `json:"quality"`
	QualityState        string    `json:"quality_state"`
	QualityEvidenceMass float64   `json:"quality_evidence_mass"`
	ContributionTotal   int       `json:"contribution_total"`
	EvaluationWeight    float64   `json:"evaluation_weight"`
	RunID               uuid.UUID `json:"run_id"`
	CalculatedAt        time.Time `json:"calculated_at"`
}

type BlogSnapshotDTO struct {
	PostID           uuid.UUID `json:"post_id"`
	ValidRatingCount int       `json:"valid_rating_count"`
	RawScore         float64   `json:"raw_score"`
	WeightedScore    *float64  `json:"weighted_score,omitempty"`
	WeightSum        float64   `json:"weight_sum"`
	QualityEstimate  float64   `json:"quality_estimate"`
	QualityActive    bool      `json:"quality_active"`
	RunID            uuid.UUID `json:"run_id"`
	CalculatedAt     time.Time `json:"calculated_at"`
}

// RunShadow computes and publishes a complete candidate snapshot without
// changing blog's existing public AVG(score) aggregation.
func (s *Service) RunShadow(ctx context.Context, now time.Time) (*model.ReputationRun, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("reputation database is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	run := model.ReputationRun{
		Status: model.ReputationRunRunning, QualityAlgorithmVersion: QualityAlgorithmVersionV1,
		ContributionRuleVersion: "music-contribution-v1", WeightAlgorithmVersion: WeightAlgorithmVersionV1,
		StartedAt: now,
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var published model.ReputationRun
		if err := tx.Where("status = ?", model.ReputationRunPublished).Order("published_at DESC").First(&published).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		previousQuality, err := qualityByUser(tx, published.ID)
		if err != nil {
			return err
		}
		if err := tx.Create(&run).Error; err != nil {
			return err
		}
		blogSnapshots, evidenceByUser, err := calculateBlogSnapshots(tx, run.ID, previousQuality)
		if err != nil {
			return err
		}
		if len(blogSnapshots) > 0 {
			if err := tx.Create(&blogSnapshots).Error; err != nil {
				return err
			}
		}
		userSnapshots, err := calculateUserSnapshots(tx, run.ID, evidenceByUser)
		if err != nil {
			return err
		}
		if len(userSnapshots) > 0 {
			if err := tx.Create(&userSnapshots).Error; err != nil {
				return err
			}
		}
		publishedAt := now
		return tx.Model(&run).Updates(map[string]any{
			"status": model.ReputationRunPublished, "published_at": publishedAt,
		}).Error
	}); err != nil {
		return nil, err
	}
	run.Status, run.PublishedAt = model.ReputationRunPublished, &now
	return &run, nil
}

func qualityByUser(db *gorm.DB, runID uuid.UUID) (map[uuid.UUID]float64, error) {
	result := map[uuid.UUID]float64{}
	if runID == uuid.Nil {
		return result, nil
	}
	var snapshots []model.UserReputationSnapshot
	if err := db.Where("run_id = ?", runID).Find(&snapshots).Error; err != nil {
		return nil, err
	}
	for _, snapshot := range snapshots {
		result[snapshot.UserID] = snapshot.Quality
	}
	return result, nil
}

func (s *Service) LatestUserSnapshot(ctx context.Context, userID uuid.UUID) (UserSnapshotDTO, error) {
	var snapshot model.UserReputationSnapshot
	query := s.db.WithContext(ctx).Joins("JOIN reputation_runs ON reputation_runs.id = user_reputation_snapshots.run_id").
		Where("user_reputation_snapshots.user_id = ? AND reputation_runs.status = ?", userID, model.ReputationRunPublished).
		Order("reputation_runs.published_at DESC")
	if err := query.First(&snapshot).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return UserSnapshotDTO{Quality: InitialQuality, QualityState: model.ReputationQualityInitial, EvaluationWeight: InitialQuality / 100}, nil
	} else if err != nil {
		return UserSnapshotDTO{}, err
	}
	var run model.ReputationRun
	if err := s.db.WithContext(ctx).First(&run, "id = ?", snapshot.RunID).Error; err != nil {
		return UserSnapshotDTO{}, err
	}
	return userSnapshotDTO(snapshot, run), nil
}

func (s *Service) LatestBlogSnapshot(ctx context.Context, postID uuid.UUID) (BlogSnapshotDTO, error) {
	var snapshot model.BlogQualitySnapshot
	query := s.db.WithContext(ctx).Joins("JOIN reputation_runs ON reputation_runs.id = blog_quality_snapshots.run_id").
		Where("blog_quality_snapshots.post_id = ? AND reputation_runs.status = ?", postID, model.ReputationRunPublished).
		Order("reputation_runs.published_at DESC")
	if err := query.First(&snapshot).Error; err != nil {
		return BlogSnapshotDTO{}, err
	}
	var run model.ReputationRun
	if err := s.db.WithContext(ctx).First(&run, "id = ?", snapshot.RunID).Error; err != nil {
		return BlogSnapshotDTO{}, err
	}
	return blogSnapshotDTO(snapshot, run), nil
}

type blogEvidence struct{ quality, share float64 }

func calculateBlogSnapshots(tx *gorm.DB, runID uuid.UUID, previousQuality map[uuid.UUID]float64) ([]model.BlogQualitySnapshot, map[uuid.UUID][]blogEvidence, error) {
	var users []model.User
	if err := tx.Select("uuid").Where("is_active = ?", true).Find(&users).Error; err != nil {
		return nil, nil, err
	}
	activeUsers := make(map[uuid.UUID]bool, len(users))
	for _, user := range users {
		activeUsers[user.UUID] = true
	}
	posts, err := blog.LoadCanonicalBlogPosts(tx, blog.CanonicalBlogPostsQuery(tx).
		Where("posts.status = ? AND posts.visibility = ?", "published", "public"))
	if err != nil {
		return nil, nil, err
	}

	postIDs := make([]uuid.UUID, 0, len(posts))
	for _, post := range posts {
		postIDs = append(postIDs, post.ID)
	}
	ratingsByPost := make(map[uuid.UUID][]model.PostRating, len(posts))
	if len(postIDs) > 0 {
		var ratings []model.PostRating
		if err := tx.Where("content_id IN ?", postIDs).Find(&ratings).Error; err != nil {
			return nil, nil, err
		}
		for _, rating := range ratings {
			ratingsByPost[rating.ContentID] = append(ratingsByPost[rating.ContentID], rating)
		}
	}

	snapshots := make([]model.BlogQualitySnapshot, 0, len(posts))
	evidence := make(map[uuid.UUID][]blogEvidence)
	for _, post := range posts {
		ratings := ratingsByPost[post.ID]
		var rawSum, weightedSum, weightSum float64
		count := 0
		for _, rating := range ratings {
			if rating.UserID == post.UserID || !activeUsers[rating.UserID] {
				continue
			}
			quality, exists := previousQuality[rating.UserID]
			if !exists {
				quality = InitialQuality
			}
			weight := clamp(quality, 0, 100) / 100
			count++
			rawSum += float64(rating.Score)
			weightedSum += weight * float64(rating.Score)
			weightSum += weight
		}
		raw := 0.0
		if count > 0 {
			raw = rawSum / float64(count)
		}
		qualityEstimate := BlogPriorMean
		var weighted *float64
		if weightSum > 0 {
			value := weightedSum / weightSum
			weighted = &value
			qualityEstimate = (BlogPriorStrength*BlogPriorMean + 10*weightedSum) / (BlogPriorStrength + weightSum)
		}
		active := count >= BlogMinimumRatingCount && weightSum > 0
		snapshot := model.BlogQualitySnapshot{
			RunID: runID, PostID: post.ID, ValidRatingCount: count, RawScore: raw,
			WeightedScore: weighted, WeightSum: weightSum, QualityEstimate: qualityEstimate,
			QualityActive: active, PriorMean: BlogPriorMean, PriorStrength: BlogPriorStrength,
		}
		snapshots = append(snapshots, snapshot)
		if active {
			evidence[post.UserID] = append(evidence[post.UserID], blogEvidence{quality: qualityEstimate, share: 1})
		}
	}
	return snapshots, evidence, nil
}

func calculateUserSnapshots(tx *gorm.DB, runID uuid.UUID, evidenceByUser map[uuid.UUID][]blogEvidence) ([]model.UserReputationSnapshot, error) {
	var users []model.User
	if err := tx.Select("uuid").Where("is_active = ?", true).Find(&users).Error; err != nil {
		return nil, err
	}
	contributions, err := activeContributionByUser(tx)
	if err != nil {
		return nil, err
	}
	snapshots := make([]model.UserReputationSnapshot, 0, len(users))
	for _, user := range users {
		evidence := evidenceByUser[user.UUID]
		sort.Slice(evidence, func(i, j int) bool { return evidence[i].quality > evidence[j].quality })
		mass, portfolio, stabilityRate, penalty := qualityFromEvidence(evidence)
		state := model.ReputationQualityInitial
		if mass > 0 {
			state = model.ReputationQualityEvidenceBased
		}
		quality := clamp(portfolio-penalty, 0, 100)
		snapshots = append(snapshots, model.UserReputationSnapshot{
			RunID: runID, UserID: user.UUID, ContributionTotal: contributions[user.UUID], QualityState: state,
			QualityEvidenceMass: mass, PortfolioQuality: portfolio, StabilityRate: stabilityRate,
			StabilityPenalty: penalty, Quality: quality, EvaluationWeight: quality / 100,
		})
	}
	return snapshots, nil
}

func activeContributionByUser(tx *gorm.DB) (map[uuid.UUID]int, error) {
	result := map[uuid.UUID]int{}
	if !tx.Migrator().HasTable(&model.MusicContributionEvidence{}) {
		return result, nil
	}
	type row struct {
		ActorUserID uuid.UUID
		Total       int
	}
	var rows []row
	if err := tx.Model(&model.MusicContributionEvidence{}).
		Select("actor_user_id, COALESCE(SUM(points), 0) AS total").Where("status = ?", model.ContributionEvidenceActive).
		Group("actor_user_id").Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.ActorUserID] = row.Total
	}
	return result, nil
}

func qualityFromEvidence(evidence []blogEvidence) (mass, portfolio, stabilityRate, penalty float64) {
	portfolio = InitialQuality
	for _, item := range evidence {
		mass += item.share
		weight := math.Pow(QualityDecay, mass-item.share) * (1 - math.Pow(QualityDecay, item.share))
		portfolio += weight * (item.quality - InitialQuality)
	}
	if mass == 0 {
		return 0, InitialQuality, 0.8, 0
	}
	qualified := 0.0
	for _, item := range evidence {
		qualified += item.share * clamp((item.quality-45)/15, 0, 1)
	}
	stabilityRate = (4 + qualified) / (5 + mass)
	penalty = 20 * mass / (mass + 5) * math.Pow(clamp((0.65-stabilityRate)/0.45, 0, 1), 2)
	return mass, portfolio, stabilityRate, penalty
}

func clamp(value, low, high float64) float64 { return math.Max(low, math.Min(high, value)) }

func userSnapshotDTO(snapshot model.UserReputationSnapshot, run model.ReputationRun) UserSnapshotDTO {
	return UserSnapshotDTO{Quality: snapshot.Quality, QualityState: snapshot.QualityState, QualityEvidenceMass: snapshot.QualityEvidenceMass, ContributionTotal: snapshot.ContributionTotal, EvaluationWeight: snapshot.EvaluationWeight, RunID: run.ID, CalculatedAt: *run.PublishedAt}
}

func blogSnapshotDTO(snapshot model.BlogQualitySnapshot, run model.ReputationRun) BlogSnapshotDTO {
	return BlogSnapshotDTO{PostID: snapshot.PostID, ValidRatingCount: snapshot.ValidRatingCount, RawScore: snapshot.RawScore, WeightedScore: snapshot.WeightedScore, WeightSum: snapshot.WeightSum, QualityEstimate: snapshot.QualityEstimate, QualityActive: snapshot.QualityActive, RunID: run.ID, CalculatedAt: *run.PublishedAt}
}

func (s *Service) RunID(ctx context.Context) (uuid.UUID, error) {
	var run model.ReputationRun
	if err := s.db.WithContext(ctx).Where("status = ?", model.ReputationRunPublished).Order("published_at DESC").First(&run).Error; err != nil {
		return uuid.Nil, err
	}
	return run.ID, nil
}
