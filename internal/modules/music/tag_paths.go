package music

import "atoman/internal/model"

// 分类深度已有三级约束，不递归加载整棵树。
func (s *Service) musicTagPath(tag model.MusicTag) []string {
	path := []string{tag.Name}
	for tag.ParentID != nil && len(path) < musicTagMaxDepth {
		var parent model.MusicTag
		if s.db.First(&parent, "id=?", tag.ParentID).Error != nil {
			break
		}
		path = append([]string{parent.Name}, path...)
		tag = parent
	}
	return path
}
