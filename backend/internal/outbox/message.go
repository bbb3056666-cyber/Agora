package outbox

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"
)

// Message 是通用事务发件箱记录：业务状态与待投递事件在同一 MySQL 事务提交。
// 每种事件都将完整消息放进 Payload，Poller 仅按 EventType 选择对应发布器。
type Message struct {
	ID          uint            `gorm:"primaryKey"`
	EventType   string          `gorm:"type:varchar(50);index"` //消息类型:content_published，like_intent，like_applied，comment_intent，comment_applied
	Payload     json.RawMessage `gorm:"type:json"`              //事件的JSON，消息真正内容
	Status      string          `gorm:"type:varchar(50);index"` //pending
	RetryCount  int             `gorm:"not null;default:0"`     //为延迟重试预留
	NextRetryAt *time.Time      `gorm:"index"`                  //为延迟重试预留
	PublishedAt *time.Time      //记录 Broker Confirm 成功时刻。
	CreateTime  time.Time       `gorm:"autoCreateTime"`
}

func (Message) TableName() string {
	return "outbox_msgs"
}

// NewEventID 生成落入 Outbox 的业务事件唯一标识。
func NewEventID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
