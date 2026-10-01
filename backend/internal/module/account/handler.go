package account

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bbb3056666-cyber/Agora/backend/internal/apierror"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

//HTTP 请求处理

// ✅
type AccountHandler struct {
	accountService *AccountService
}

// ✅
func NewAccountHandler(accountService *AccountService) *AccountHandler {
	return &AccountHandler{accountService: accountService}
}

// ✅
func (h *AccountHandler) CreateAccount(c *gin.Context) {
	var req CreateAccountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		//绑定失败,识别错误码
		c.JSON(apierror.ClassifyHTTPStatus(err), gin.H{"error": err.Error()})
		return
	}
	// c.Request.Context() 跟请求同生共死，传给 Service → Repo → GORM，请求取消则 SQL 也中断
	if err := h.accountService.CreateAccount(c.Request.Context(), &Account{Username: req.Username, Password: req.Password}); err != nil {
		if errors.Is(err, ErrUsernameTaken) {
			c.JSON(http.StatusConflict, gin.H{
				"error": "username already exists",
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	c.JSON(200, gin.H{"message": "account created"})
}

// ✅
// 用户名重命名
func (h *AccountHandler) Rename(c *gin.Context) {
	var req RenameRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(apierror.ClassifyHTTPStatus(err), gin.H{"error": err.Error()})
		return
	}
	accountID, err := GetAccountID(c)
	if err != nil {
		c.JSON(apierror.ClassifyHTTPStatus(err), gin.H{"error": err.Error()})
		return
	}
	token, err := h.accountService.Rename(c.Request.Context(), accountID, req.NewUsername)
	if err != nil {
		//新用户名不存在
		if errors.Is(err, ErrNewUsernameRequired) {
			c.JSON(apierror.ClassifyHTTPStatus(err), gin.H{"error": err.Error()})
			return
		}
		//用户名重复
		if errors.Is(err, ErrUsernameTaken) {
			c.JSON(409, gin.H{"error": err.Error()})
			return
		}
		//未找到账户
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(404, gin.H{"error": "account not found"})
			return
		}
		//其他错误
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"token": token})
}

// ✅
func (h *AccountHandler) ChangePassword(c *gin.Context) {
	var req ChangePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(apierror.ClassifyHTTPStatus(err), gin.H{"error": err.Error()})
		return
	}
	if err := h.accountService.ChangePassword(c.Request.Context(), req.Username, req.OldPassword, req.NewPassword); err != nil {
		c.JSON(400, gin.H{"error": "unsuccessfully password changed"})
		return
	}
	c.JSON(200, gin.H{"message": "successfully password changed"})
}

// ✅
// 根据id查找用户,方便前端得到用户信息
func (h *AccountHandler) FindByID(c *gin.Context) {
	var req FindByIDRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(apierror.ClassifyHTTPStatus(err), gin.H{"error": err.Error()})
		return
	}
	if account, err := h.accountService.FindByID(c.Request.Context(), req.ID); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	} else {
		c.JSON(200, account)
	}
}

// ✅
// 用户名找账户
func (h *AccountHandler) FindByUsername(c *gin.Context) {
	var req FindByUsernameRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(apierror.ClassifyHTTPStatus(err), gin.H{"error": err.Error()})
		return
	}
	if account, err := h.accountService.FindByUsername(c.Request.Context(), req.Username); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	} else {
		c.JSON(200, account)
	}
}

// ✅
func (h *AccountHandler) Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(apierror.ClassifyHTTPStatus(err), gin.H{"error": err.Error()})
		return
	}
	//根据用户名找到账户对象
	account, err := h.accountService.FindByUsername(c.Request.Context(), req.Username)
	if err != nil { //没找到/数据库连接错误/context取消
		//500表示服务器错误
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	accessToken, refreshToken, err := h.accountService.Login(c.Request.Context(), account, req.Password)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}
	//返回两个token,id,用户名
	c.JSON(200, LoginResponse{Token: accessToken, RefreshToken: refreshToken, AccountID: account.ID, Username: account.Username})
}

// ✅
// 登出,先获取id,后调用service登出
func (h *AccountHandler) Logout(c *gin.Context) {
	//获取refreshToken,确定是哪个设备要登出
	var req RefreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(apierror.ClassifyHTTPStatus(err), gin.H{"error": err.Error()})
		return
	}
	//从上下文拿jti和过期时间(中间件check已解析),用于注册黑名单
	jtiVal, _ := c.Get("jti")
	expVal, _ := c.Get("exp")
	jtiStr, _ := jtiVal.(string)
	//exp可能没设置(旧token无过期时间),兜底15分钟
	ttl := 15 * time.Minute
	if expTime, ok := expVal.(time.Time); ok {
		ttl = time.Until(expTime)
		if ttl <= 0 {
			ttl = 15 * time.Minute
		}
	}
	if err := h.accountService.Logout(c.Request.Context(), req.RefreshToken, jtiStr, ttl); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"message": "account logged out"})
}

// ✅
// 保存头像到磁盘,创建头像存储URL,上传URL至数据库
func (h *AccountHandler) UploadAvatar(c *gin.Context) {
	//从上下文获取id
	accountID, err := GetAccountID(c)
	//没登录/过期
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}
	//表单数据,上传文件
	//application/x-www-form-urlencoded	纯文本表单   c.PostForm("key")
	//multipart/form-data				带文件上传  	c.FormFile("file")
	f, err := c.FormFile("file")
	//请求有问题
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing file"})
		return
	}
	//校验文件大小
	const maxSize = 10 << 20
	if f.Size <= 0 || f.Size > maxSize {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid file size"})
		return
	}
	//获取文件后缀
	ext := strings.ToLower(filepath.Ext(f.Filename))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".webp":
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "only .jpg/.jpeg/.png/.webp allowed"})
		return
	}
	//拼本地路径: .run/uploads/avatars/1
	dir := filepath.Join(".run", "uploads", "avatars", strconv.FormatUint(uint64(accountID), 10))
	//创建目录（已存在就跳过，不报错）
	if err := os.MkdirAll(dir, 0o755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	//生成 16 字节随机数 → 32 位十六进制字符串（"a1b2c3..."）
	filename, err := randHex(16)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	//加上扩展名: "a1b2c3d4.jpg"
	filename = filename + ext
	//拼完整路径: .run/uploads/avatars/1/a1b2c3d4.jpg
	absPath := filepath.Join(dir, filename)
	//把上传的文件保存到磁盘
	if err := c.SaveUploadedFile(f, absPath); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	//拼 URL 路径（用 path.Join，永远正斜杠）: /static/avatars/1/a1b2c3d4.jpg
	urlPath := path.Join("/static", "avatars", strconv.FormatUint(uint64(accountID), 10), filename)
	//包成完整 URL: http://localhost:8080/static/avatars/1/a1b2c3d4.jpg
	avatarURL := buildAbsoluteURL(c, urlPath)
	if err := h.accountService.UpdateAvatar(c.Request.Context(), accountID, avatarURL); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"avatar_url": avatarURL})
}

// ✅
// 更新个人资料,头像URL,个人简介
func (h *AccountHandler) UpdateProfile(c *gin.Context) {
	accountID, err := GetAccountID(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}
	var req UpdateProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(apierror.ClassifyHTTPStatus(err), gin.H{"error": err.Error()})
		return
	}
	if err := h.accountService.UpdateProfile(c.Request.Context(), accountID, &req); err != nil {
		c.JSON(apierror.ClassifyHTTPStatus(err), gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "profile updated"})
}

// ✅
// access token 过期后，前端自动用 refresh token 换新 token，用户无感知
// refresh token也更新
func (h *AccountHandler) Refresh(c *gin.Context) {
	var req RefreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(apierror.ClassifyHTTPStatus(err), gin.H{"error": err.Error()})
		return
	}
	newAccessToken, newRefreshToken, accountID, username, err := h.accountService.RefreshAccessToken(c.Request.Context(), req.RefreshToken)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid refresh token"})
		return
	}
	c.JSON(http.StatusOK, LoginResponse{Token: newAccessToken, RefreshToken: newRefreshToken, AccountID: accountID, Username: username})
}

// ✅
// 生成2n个随机字符
func randHex(n int) (string, error) {
	//n字节数组
	b := make([]byte, n)
	//填入随机字节
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand.Read: %w", err)
	}
	//转换成16进制字符串
	return hex.EncodeToString(b), nil
}

// ✅
// 拼头像完整URL,存入数据库
func buildAbsoluteURL(c *gin.Context, p string) string {
	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	if xf := c.GetHeader("X-Forwarded-Proto"); xf != "" {
		scheme = xf
	}
	return fmt.Sprintf("%s://%s%s", scheme, c.Request.Host, p)
}

// ✅
// 取出中间件解析JWT令牌存入请求上下文的accountID
func GetAccountID(c *gin.Context) (uint, error) {
	uidValue, exists := c.Get("accountID")
	if !exists { //不存在
		return 0, errors.New("accountID not found")
	}

	accountID, ok := uidValue.(uint)
	if !ok { //accountID格式不对
		return 0, errors.New("accountID has invalid type")
	}

	return accountID, nil
}
