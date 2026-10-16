package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// TokenType 区分 access / refresh token。
type TokenType string

const (
	TokenAccess  TokenType = "access"
	TokenRefresh TokenType = "refresh"
)

// Claims 是 RecMaestro 的 JWT 载荷。
type Claims struct {
	UserID   uint64    `json:"uid"`
	Username string    `json:"username"`
	Role     string    `json:"role"`
	Type     TokenType `json:"type"`
	jwt.RegisteredClaims
}

// Manager 负责 JWT 的签发与校验。
type Manager struct {
	secret        []byte
	accessExpire  time.Duration
	refreshExpire time.Duration
}

// NewManager 创建 JWT Manager。access/refresh 使用不同有效期。
func NewManager(secret string, accessExpire, refreshExpire time.Duration) *Manager {
	return &Manager{
		secret:        []byte(secret),
		accessExpire:  accessExpire,
		refreshExpire: refreshExpire,
	}
}

// SignAccess 签发 access token。
func (m *Manager) SignAccess(userID uint64, username, role string) (string, error) {
	return m.sign(userID, username, role, TokenAccess, m.accessExpire)
}

// SignRefresh 签发 refresh token。
func (m *Manager) SignRefresh(userID uint64, username, role string) (string, error) {
	return m.sign(userID, username, role, TokenRefresh, m.refreshExpire)
}

func (m *Manager) sign(userID uint64, username, role string, typ TokenType, expire time.Duration) (string, error) {
	now := time.Now()
	claims := Claims{
		UserID:   userID,
		Username: username,
		Role:     role,
		Type:     typ,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "recmaestro",
			Subject:   username,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(expire)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(m.secret)
}

// Parse 解析并校验 token，返回载荷。
func (m *Manager) Parse(tokenStr string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("非预期的签名方法: %v", t.Header["alg"])
		}
		return m.secret, nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, errors.New("无效的 token")
	}
	return claims, nil
}
