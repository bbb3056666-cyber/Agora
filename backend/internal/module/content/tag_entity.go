package content

import "regexp"

type Tag struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Name string `gorm:"uniqueIndex;type:varchar(100);not null" json:"name"`
}

type ContentTag struct {
	ID        uint `gorm:"primaryKey"`
	ContentID uint `gorm:"not null;uniqueIndex:ux_content_tag,priority:1;index"`
	TagID     uint `gorm:"not null;uniqueIndex:ux_content_tag,priority:2;index"`
}

var tagRegex = regexp.MustCompile(`#([\p{L}\p{N}_]+)`)

// ✅
// 返回标签数组
func ExtractTags(text string) []string {
	//二维数组
	// matches = [
	//   ["#美食", "美食"],       ← 每个匹配项：[全匹配, 第一组(括号里的)]
	//   ["#旅行", "旅行"],
	//   ["#Go语言教程", "Go语言教程"],
	// ]
	matches := tagRegex.FindAllStringSubmatch(text, -1)
	seen := make(map[string]bool)
	var tags []string
	for _, m := range matches {
		tag := m[1]
		if !seen[tag] {
			seen[tag] = true
			tags = append(tags, tag)
		}
	}
	return tags
}
