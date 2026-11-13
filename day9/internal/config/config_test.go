package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 使用临时文件，避免测试依赖本机 .env 或输出真实密码。
func prepareEnv(t *testing.T, contents string) {
	t.Helper()
	for _, key := range []string{
		"HTTP_ADDR", "HTTP_READ_HEADER_TIMEOUT", "HTTP_WRITE_TIMEOUT", "HTTP_IDLE_TIMEOUT",
		"DATABASE_URL", "PG_MAX_CONNS", "PG_MIN_CONNS", "PG_PING_TIMEOUT",
		"REDIS_ADDR", "REDIS_USERNAME", "REDIS_PASSWORD", "REDIS_DB", "REDIS_PING_TIMEOUT",
		"STARTUP_TIMEOUT",
	} {
		// 登记环境恢复，再删除变量，使 godotenv.Load 能从文件加载。
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	dir := filepath.Join(root, "day9", "internal", "config")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "day9", ".env"), []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
}

func TestEnvConfig(t *testing.T) {
	prepareEnv(t, `HTTP_ADDR=:9090
HTTP_READ_HEADER_TIMEOUT=2s
HTTP_WRITE_TIMEOUT=12s
HTTP_IDLE_TIMEOUT=90s
DATABASE_URL=postgres://test:test@localhost/test
PG_MAX_CONNS=8
PG_MIN_CONNS=2
PG_PING_TIMEOUT=4s
REDIS_ADDR=localhost:6380
REDIS_USERNAME=test-user
REDIS_PASSWORD=test-password
REDIS_DB=2
REDIS_PING_TIMEOUT=1500ms
STARTUP_TIMEOUT=20s
`)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("配置加载失败：%v", err)
	}
	want := Config{
		Http: HttConfig{Addr: ":9090", ReadHeaderTimeout: 2 * time.Second,
			WriteTimeout: 12 * time.Second, IdleTimeout: 90 * time.Second},
		Postgres: PostgresConfig{DSN: "postgres://test:test@localhost/test", MaxConns: 8,
			MinConns: 2, PingTimeout: 4 * time.Second},
		Redis: RedisConfig{Addr: "localhost:6380", Username: "test-user", Password: "test-password",
			DB: 2, PingTimeout: 1500 * time.Millisecond},
		StartupTimeout: 20 * time.Second,
	}
	if cfg != want {
		t.Fatal("加载结果与 .env 中配置的值不一致")
	}
}

func TestLoadDefaults(t *testing.T) {
	prepareEnv(t, "")
	t.Setenv("DATABASE_URL", "postgres://test:test@localhost/test")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("默认配置加载失败：%v", err)
	}
	if cfg.Http.Addr != ":8080" || cfg.Http.ReadHeaderTimeout != 5*time.Second ||
		cfg.Http.WriteTimeout != 10*time.Second || cfg.Http.IdleTimeout != 60*time.Second ||
		cfg.Postgres.MaxConns != 4 || cfg.Postgres.MinConns != 0 ||
		cfg.Postgres.PingTimeout != 5*time.Second || cfg.Redis.Addr != "127.0.0.1:6379" ||
		cfg.Redis.Username != "default" || cfg.Redis.DB != 0 ||
		cfg.Redis.PingTimeout != 3*time.Second || cfg.StartupTimeout != 15*time.Second {
		t.Fatal("缺省配置未使用约定的默认值")
	}
}

func TestLoadInvalidValues(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"HTTP_READ_HEADER_TIMEOUT", "abc"},
		{"HTTP_WRITE_TIMEOUT", "10"},
		{"HTTP_IDLE_TIMEOUT", "invalid"},
		{"PG_MIN_CONNS", "abc"},
		{"PG_MIN_CONNS", "2147483648"},
		{"PG_MIN_CONNS", "5"},
		{"PG_PING_TIMEOUT", "bad"},
		{"REDIS_PING_TIMEOUT", "bad"},
		{"STARTUP_TIMEOUT", "bad"},
	} {
		t.Run(tc.key+"/"+tc.value, func(t *testing.T) {
			prepareEnv(t, "")
			t.Setenv("DATABASE_URL", "postgres://test:test@localhost/test")
			t.Setenv(tc.key, tc.value)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("应返回包含 %s 的错误，实际为 %v", tc.key, err)
			}
		})
	}
}
