package content

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/bbb3056666-cyber/Agora/backend/internal/apierror"
	"github.com/bbb3056666-cyber/Agora/backend/internal/delivery/jwt"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/account"

	"github.com/gin-gonic/gin"
)

type ContentHandler struct {
	contentService *ContentService
	accountService *account.AccountService
}

func NewContentHandler(contentService *ContentService, accountService *account.AccountService) *ContentHandler {
	return &ContentHandler{contentService: contentService, accountService: accountService}
}

// ✅
// 真正入表
func (vh *ContentHandler) PublishContent(c *gin.Context) {
	var req PublishContentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(apierror.ClassifyHTTPStatus(err), gin.H{"error": err.Error()})
		return
	}

	authorId, err := jwt.GetAccountID(c)
	if err != nil {
		c.JSON(apierror.ClassifyHTTPStatus(err), gin.H{"error": err.Error()})
		return
	}
	//根据id获取用户信息
	acc, err := vh.accountService.FindByID(c.Request.Context(), authorId)
	if err != nil {
		c.JSON(apierror.ClassifyHTTPStatus(err), gin.H{"error": err.Error()})
	}
	username := acc.Username
	assets := make([]ContentAsset, 0, len(req.ImageURLs))
	for index, imageURL := range req.ImageURLs {
		assets = append(assets, ContentAsset{AssetType: ContentTypeImage, URL: imageURL, SortOrder: index})
	}
	content := &Content{
		AuthorID:    authorId,
		Username:    username,
		ContentType: req.ContentType,
		Title:       req.Title,
		Description: req.Description,
		Body:        req.Body,
		PlayURL:     req.PlayURL,
		CoverURL:    req.CoverURL,
		CreateTime:  time.Now(),
		Assets:      assets,
	}
	if err := vh.contentService.Publish(c.Request.Context(), content); err != nil {
		c.JSON(apierror.ClassifyHTTPStatus(err), gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, content)
}

// UploadImages 接收一次请求中的多张图片，返回保持原顺序的 URL 列表。
// 发布图片内容时，再把这些 URL 放进 image_urls 字段。
func (vh *ContentHandler) UploadImages(c *gin.Context) {
	authorID, err := jwt.GetAccountID(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	form, err := c.MultipartForm()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "multipart form is required"})
		return
	}
	files := form.File["files"]
	if len(files) == 0 || len(files) > 9 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "1 to 9 images are required"})
		return
	}

	date := time.Now().Format("20060102")
	relDir := filepath.Join("images", fmt.Sprintf("%d", authorID), date)
	absDir := filepath.Join(".run", "uploads", relDir)
	if err := os.MkdirAll(absDir, 0o755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	urls := make([]string, 0, len(files))
	for _, file := range files {
		if file.Size <= 0 || file.Size > 10<<20 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "each image must be no larger than 10MB"})
			return
		}
		ext := strings.ToLower(filepath.Ext(file.Filename))
		switch ext {
		case ".jpg", ".jpeg", ".png", ".webp":
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": "only .jpg/.jpeg/.png/.webp is allowed"})
			return
		}
		name, err := randHex(16)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate filename"})
			return
		}
		name += ext
		if err := c.SaveUploadedFile(file, filepath.Join(absDir, name)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		urlPath := path.Join("/static", "images", fmt.Sprintf("%d", authorID), date, name)
		urls = append(urls, buildAbsoluteURL(c, urlPath))
	}
	c.JSON(http.StatusOK, gin.H{"image_urls": urls})
}

// ✅
// 下载视频到服务器,拼接地址,返回路径
func (vh *ContentHandler) UploadVideoAsset(c *gin.Context) {
	authorId, err := jwt.GetAccountID(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	//拿到FileHeader
	f, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing file"})
		return
	}

	//检验视频大小
	const maxSize = 200 << 20
	if f.Size <= 0 || f.Size > maxSize {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid file size"})
		return
	}

	//取扩展名转小写: "VIDEO.MP4" → ".mp4"
	ext := strings.ToLower(filepath.Ext(f.Filename))
	if ext != ".mp4" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "only .mp4 is allowed"})
		return
	}

	date := time.Now().Format("20060102")
	//本地路径:videos/1/20260712/
	relDir := filepath.Join("videos", fmt.Sprintf("%d", authorId), date)
	// .run/uploads/
	root := filepath.Join(".run", "uploads")
	// .run/uploads/videos/1/20260712/
	absDir := filepath.Join(root, relDir)
	//创建文件夹
	if err := os.MkdirAll(absDir, 0o755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	//32个字符的随机字符串作为文件名:a1b2c3d4e5f60708192a0b1c2d3e4f50617
	filename, err := randHex(16)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate filename"})
		return
	}

	//a1b2c3d4e5f60708192a0b1c2d3e4f50617.mp4
	filename = filename + ext

	//最终文件地址
	absPath := filepath.Join(absDir, filename)

	// SaveUploadedFile: 把用户上传的文件从临时内存/临时文件 写入目标磁盘路径
	if err := c.SaveUploadedFile(f, absPath); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	//  磁盘 .run/uploads/videos/1/20260712/a1b2c3d4.mp4
	//  URL  /static/videos/1/20260712/a1b2c3d4.mp4
	//  r.Static("/static", "./.run/uploads") 自动映射，浏览器访问该 URL 即可播放
	urlPath := path.Join("/static", "videos", fmt.Sprintf("%d", authorId), date, filename)

	c.JSON(http.StatusOK, gin.H{
		"url":      buildAbsoluteURL(c, urlPath),
		"play_url": buildAbsoluteURL(c, urlPath),
	})
}

// ✅
// 下载封面到服务器,拼接地址,返回路径
func (vh *ContentHandler) UploadCover(c *gin.Context) {
	authorId, err := jwt.GetAccountID(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	f, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing file"})
		return
	}

	//检查文件大小
	const maxSize = 10 << 20
	if f.Size <= 0 || f.Size > maxSize {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid file size"})
		return
	}

	//后缀
	ext := strings.ToLower(filepath.Ext(f.Filename))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".webp":
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "only .jpg/.jpeg/.png/.webp is allowed"})
		return
	}

	date := time.Now().Format("20060102")
	relDir := filepath.Join("covers", fmt.Sprintf("%d", authorId), date)
	root := filepath.Join(".run", "uploads")
	absDir := filepath.Join(root, relDir)
	if err := os.MkdirAll(absDir, 0o755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	filename, err := randHex(16)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate filename"})
		return
	}
	filename = filename + ext
	absPath := filepath.Join(absDir, filename)

	//SaveUploadedFile 是 *gin.Context 的方法
	//文件数据在 Gin 解析请求时已经拿到了——要么在内存 byte 数组里，要么在临时文件里
	if err := c.SaveUploadedFile(f, absPath); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	urlPath := path.Join("/static", "covers", fmt.Sprintf("%d", authorId), date, filename)

	c.JSON(http.StatusOK, gin.H{
		"url":       buildAbsoluteURL(c, urlPath),
		"cover_url": buildAbsoluteURL(c, urlPath),
	})
}

// 返回长度为2n的随机十六进制字符串
func randHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand.Read: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// 构造绝对路径
func buildAbsoluteURL(c *gin.Context, p string) string {
	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	if xf := c.GetHeader("X-Forwarded-Proto"); xf != "" {
		scheme = xf
	}
	//scheme (http) + "://" + Host (localhost:8080) + path (/static/videos/...)
	//→ "http://localhost:8080/static/videos/1/20260712/a.mp4"
	return fmt.Sprintf("%s://%s%s", scheme, c.Request.Host, p)
}

func (vh *ContentHandler) DeleteContent(c *gin.Context) {
	var req DeleteContentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(apierror.ClassifyHTTPStatus(err), gin.H{"error": err.Error()})
		return
	}
	authorId, err := jwt.GetAccountID(c)
	if err != nil {
		c.JSON(apierror.ClassifyHTTPStatus(err), gin.H{"error": err.Error()})
		return
	}
	if err := vh.contentService.Delete(c.Request.Context(), req.ID, authorId); err != nil {
		c.JSON(apierror.ClassifyHTTPStatus(err), gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"message": "content deleted"})
}

// ✅
// 根据账户id获取视频列表
func (vh *ContentHandler) ListByAuthorID(c *gin.Context) {
	var req ListByAuthorIDRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(apierror.ClassifyHTTPStatus(err), gin.H{"error": err.Error()})
		return
	}
	videos, err := vh.contentService.ListByAuthorID(c.Request.Context(), req.AuthorID)
	if err != nil {
		c.JSON(apierror.ClassifyHTTPStatus(err), gin.H{"error": err.Error()})
		return
	}
	if videos == nil {
		videos = []Content{}
	}
	c.JSON(200, videos)
}

// ✅
func (vh *ContentHandler) GetDetail(c *gin.Context) {
	var req GetDetailRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(apierror.ClassifyHTTPStatus(err), gin.H{"error": err.Error()})
		return
	}
	//根据id获取视频全部信息
	video, err := vh.contentService.GetDetail(c.Request.Context(), req.ID)
	if err != nil {
		c.JSON(apierror.ClassifyHTTPStatus(err), gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, video)
}

func (vh *ContentHandler) UpdateLikesCount(c *gin.Context) {
	var req UpdateLikesCountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(apierror.ClassifyHTTPStatus(err), gin.H{"error": err.Error()})
		return
	}
	if err := vh.contentService.UpdateLikesCount(c.Request.Context(), req.ID, req.LikesCount); err != nil {
		c.JSON(apierror.ClassifyHTTPStatus(err), gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"message": "likes count updated"})
}
