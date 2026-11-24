package papers

import (
	"context"
	"errors"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type approvalMailFixture struct {
	count          int
	recipient, key string
	failure        bool
}

func (m *approvalMailFixture) SendPapersApproval(_ context.Context, to, key string) error {
	m.count++
	m.recipient, m.key = to, key
	if m.failure {
		return errors.New("fixture transport failure")
	}
	return nil
}
func TestApplicationReviewGrantAndDurableApprovalMail(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.PaperAccessGrant{}, &model.PaperAccessApplication{}, &model.AuditLog{})
	makeUser := func(name, role string) model.User {
		u := model.User{Username: name, Email: uuid.NewString() + "@example.invalid", Password: "fixture", Role: role, IsActive: true}
		require.NoError(t, db.Create(&u).Error)
		return u
	}
	owner := makeUser("papers-applicant", "user")
	reviewer := makeUser("papers-admin", "admin")
	other := makeUser("papers-other", "user")
	u := authctx.CurrentUser{ID: owner.UUID, Role: "user"}
	admin := authctx.CurrentUser{ID: reviewer.UUID, Role: "admin"}
	outsider := authctx.CurrentUser{ID: other.UUID, Role: "user"}
	svc := NewService(db)
	missing, err := svc.Application(u)
	require.NoError(t, err)
	require.Nil(t, missing)
	_, err = svc.Apply(u, "abc")
	require.Error(t, err)
	a, err := svc.Apply(u, "科研论文在线写作")
	require.NoError(t, err)
	require.Equal(t, "pending", a.Status)
	repeat, err := svc.Apply(u, "重复申请也不能生成第二条")
	require.NoError(t, err)
	require.Equal(t, a.ID, repeat.ID)
	require.Error(t, svc.RequireAccess(u))
	_, err = svc.Applications(outsider, "pending", 1)
	require.Error(t, err)
	_, err = svc.ReviewApplication(outsider, a.ID, "approved", "")
	require.Error(t, err)
	unseen, err := svc.Application(outsider)
	require.NoError(t, err)
	require.Nil(t, unseen)
	list, err := svc.Applications(admin, "pending", 1)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, owner.Username, list[0].Username)
	approved, err := svc.ReviewApplication(admin, a.ID, "approved", "")
	require.NoError(t, err)
	require.Equal(t, "approved", approved.Status)
	require.Equal(t, "pending", approved.NotificationStatus)
	require.NoError(t, svc.RequireAccess(u))
	_, err = svc.ReviewApplication(admin, a.ID, "approved", "")
	require.NoError(t, err)
	_, err = svc.ReviewApplication(admin, a.ID, "rejected", "late")
	require.Error(t, err)
	mailer := &approvalMailFixture{failure: true}
	worked, err := DeliverApprovalMail(context.Background(), db, mailer)
	require.NoError(t, err)
	require.True(t, worked)
	require.Equal(t, owner.Email, mailer.recipient)
	require.Equal(t, "papers-approval-"+a.ID.String(), mailer.key)
	actual, err := svc.Application(u)
	require.NoError(t, err)
	require.Equal(t, "pending", actual.NotificationStatus)
	require.NoError(t, db.Model(&model.PaperAccessApplication{}).Where("id=?", a.ID).Update("notification_next_at", time.Now().Add(-time.Minute)).Error)
	mailer.failure = false
	worked, err = DeliverApprovalMail(context.Background(), db, mailer)
	require.NoError(t, err)
	require.True(t, worked)
	actual, err = svc.Application(u)
	require.NoError(t, err)
	require.Equal(t, "sent", actual.NotificationStatus)
	require.NotNil(t, actual.NotificationSentAt)
	worked, err = DeliverApprovalMail(context.Background(), db, mailer)
	require.NoError(t, err)
	require.False(t, worked)
	require.Equal(t, 2, mailer.count)
	_, err = svc.Apply(u, "再次申请")
	require.Error(t, err)
	// A rejected applicant is not granted access or queued for mail.
	b, err := svc.Apply(outsider, "申请测试写作")
	require.NoError(t, err)
	_, err = svc.ReviewApplication(admin, b.ID, "rejected", "")
	require.Error(t, err)
	rejected, err := svc.ReviewApplication(admin, b.ID, "rejected", "暂不符合要求")
	require.NoError(t, err)
	require.Equal(t, "rejected", rejected.Status)
	require.Error(t, svc.RequireAccess(outsider))
	_, err = svc.Apply(outsider, "刚刚拒绝不能重复申请")
	require.Error(t, err)
	worked, err = DeliverApprovalMail(context.Background(), db, mailer)
	require.NoError(t, err)
	require.False(t, worked)
	require.Equal(t, 2, mailer.count)
}
