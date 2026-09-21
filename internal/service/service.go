package service

import (
	"time"

	"example.com/batch-092001-q003/internal/domain"
	"example.com/batch-092001-q003/internal/store"
)

// Actor 是发起操作的主体，由接入层从请求头解析。
type Actor struct {
	ID            string
	Dept          domain.Department
	InstitutionID string
}

func (a Actor) validate() error {
	if a.ID == "" {
		return domain.Invalidf("缺少操作人标识（X-Actor-Id）")
	}
	return nil
}

// Service 承载全部业务规则。所有写路径在 store.Update 临界区内
// 完成“校验并落库”，保证并发操作不会越过缺失环节。
type Service struct {
	st *store.Store
}

// New 创建服务。
func New(st *store.Store) *Service { return &Service{st: st} }

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func parseTime(value string) (time.Time, error) {
	return time.Parse(time.RFC3339, value)
}
