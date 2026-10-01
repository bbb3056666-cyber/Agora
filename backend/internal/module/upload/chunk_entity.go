package upload

const ChunkSize = 5 << 20 // 5 MB

type ChunkUploadSession struct {
	UploadID     string `json:"upload_id"`    // 本次上传的唯一会话 ID
	AccountID    uint   `json:"account_id"`   // 谁在上传
	Filename     string `json:"filename"`     // 原始文件名
	FileSize     int64  `json:"file_size"`    // 总文件大小
	ChunkSize    int64  `json:"chunk_size"`   // 每个块多大
	TotalChunks  int    `json:"total_chunks"` // 一共多少个块
	FileHash     string `json:"file_hash"`    // 完整文件的 MD5
	UploadedBits []bool `json:"uploaded_bits"`
	// [true, true, false, false, ...] 40 个 bool：
	// 第 0、1 块已传，第 2、3 块还没传
}

// UploadedChunks 返回缺少的块的下标
func (s *ChunkUploadSession) UploadedChunks() []int {
	var indices []int //此时indices为nil
	for i, uploaded := range s.UploadedBits {
		if uploaded {
			indices = append(indices, i)
		}
	}
	if indices == nil {
		return []int{} //确保json中值为[]而不是null
	}
	//[0,3,4,6,7,9]
	return indices
}

// 遍历UploadedBits确认每一块为true
func (s *ChunkUploadSession) IsComplete() bool {
	for _, b := range s.UploadedBits {
		if !b {
			return false
		}
	}
	return true
}

type InitChunkUploadRequest struct {
	Filename    string `json:"filename" binding:"required"`
	FileSize    int64  `json:"file_size" binding:"required,min=1"`    //文件总大小
	ChunkSize   int64  `json:"chunk_size" binding:"required,min=1"`   //文件分块大小
	TotalChunks int    `json:"total_chunks" binding:"required,min=1"` //块数
	FileHash    string `json:"file_hash" binding:"required"`          //整个文件的 MD5，用于断点续传和完整性校验
	// MD5: 一种哈希算法，把任意大小的文件映射成一个固定长度的唯一指纹
}

type UploadChunkRequest struct {
	UploadID   string `form:"upload_id" binding:"required"`  // 会话唯一id,在redis中找到哪个会话
	ChunkIndex int    `form:"chunk_index" binding:"min=0"`   // 将要上传第几块（从 0 开始）
	ChunkHash  string `form:"chunk_hash" binding:"required"` // 这一块的 MD5，前端自己算的,要和后端计算的对比,防止文件损坏
	//用的是 form: 标签，不是 json
	//前端用 multipart/form-data 传（要传文件数据），不能用 JSON
}

type ChunkStatusRequest struct {
	UploadID string `json:"upload_id" binding:"required"`
}

type CompleteChunkUploadRequest struct {
	UploadID string `json:"upload_id" binding:"required"`
}
