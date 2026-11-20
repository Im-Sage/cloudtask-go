package config

import (
	"strings"
	"testing"
	"time"
)

// 每次返回一份合法配置
func validConfig() Config {
	return Config{
		Http: HttConfig{
			Addr:              ":8080",
			ReadHeaderTimeout: 5 * time.Second,
			WriteTimeout:      10 * time.Second,
			IdleTimeout:       60 * time.Second,
		},
		Postgres: PostgresConfig{
			DSN:         "postgres://test:test@localhost/test",
			MaxConns:    4,
			MinConns:    0,
			PingTimeout: 5 * time.Second,
		},
		Redis: RedisConfig{
			Addr:        "redis:6379",
			Username:    "default",
			Password:    "",
			DB:          0,
			PingTimeout: 3 * time.Second,
		},
		StartupTimeout: 15 * time.Second,
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name string
		// change可以保存一个接收*Config参数，没有返回值的函数
		change  func(*Config)
		wantErr string
	}{
		{
			name: "正常配置",
		},
		{
			name: "空数据库连接串",
			change: func(c *Config) {
				c.Postgres.DSN = ""
			},
			wantErr: "DATABASE_URL",
		},
		{
			name: "最大连接数为零",
			change: func(c *Config) {
				c.Postgres.MaxConns = 0
			},
			wantErr: "PG_MAX_CONNS",
		},
		{
			name: "最大连接数为负数",
			change: func(c *Config) {
				c.Postgres.MaxConns = -1
			},
			wantErr: "PG_MAX_CONNS",
		},
		{
			name: "最小连接数超过最大连接数",
			change: func(c *Config) {
				c.Postgres.MinConns = 5
			},
			wantErr: "PG_MIN_CONNS",
		},
		{
			name: "最小连接数为负数",
			change: func(c *Config) {
				c.Postgres.MinConns = -1
			},
			wantErr: "PG_MIN_CONNS",
		},
		{
			name: "Redis数据库编号为负数",
			change: func(c *Config) {
				c.Redis.DB = -1
			},
			wantErr: "REDIS_DB",
		},
		{
			name: "启动超时为零",
			change: func(c *Config) {
				c.StartupTimeout = 0
			},
			wantErr: "启动超时",
		},
		{
			name: "启动超时为负数",
			change: func(c *Config) {
				c.StartupTimeout = -time.Second
			},
			wantErr: "启动超时",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 每一个场景都从一份新的合法配置开始
			cfg := validConfig()

			// 如果这个场景需要修改配置来模仿出错环境，则执行修改函数
			if tt.change != nil {
				tt.change(&cfg)
			}
			err := cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("合法配置应通过，实际错误：%v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("应返回包含 %q 的错误，实际没有错误",
					tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("错误应包含 %q，实际为：%v",
					tt.wantErr, err)
			}
		})
	}
}

// 隔离 Load 会读取的环境变量和本地 .env。
func prepareLoadEnv(t *testing.T) {
	t.Helper() // 将函数标记为辅助函数

	// 空目录下不存在 day9/.env。
	// Load 当前会忽略“文件不存在”错误。
	t.Chdir(t.TempDir()) // 在临时目录中运行，避免读到本地day/.env

	keys := []string{
		"HTTP_ADDR",
		"HTTP_READ_HEADER_TIMEOUT",
		"HTTP_WRITE_TIMEOUT",
		"HTTP_IDLE_TIMEOUT",
		"DATABASE_URL",
		"PG_MAX_CONNS",
		"PG_MIN_CONNS",
		"PG_PING_TIMEOUT",
		"REDIS_ADDR",
		"REDIS_USERNAME",
		"REDIS_PASSWORD",
		"REDIS_DB",
		"REDIS_PING_TIMEOUT",
		"STARTUP_TIMEOUT",
	}

	for _, key := range keys {
		t.Setenv(key, "") // 覆盖环境变量
	}

	// 唯一必填的连接串使用虚构凭据。
	t.Setenv("DATABASE_URL",
		"postgres://test:test@localhost/test")
}

func TestLoadDefaults(t *testing.T) {
	prepareLoadEnv(t) // 非必需变量都为空

	got, err := Load() // 从环境变量中加载实际结果(是刚刚在prepareLoadEnv中设置的新的环境变量)
	if err != nil {
		t.Fatalf("加载默认配置失败：%v", err)
	}

	want := validConfig() // want 为合法配置
	if got != want {
		t.Fatal("加载结果与约定的默认配置不一致")
	}
}

func TestLoadValidEnv(t *testing.T) {
	prepareLoadEnv(t)

	env := map[string]string{
		"HTTP_ADDR":                ":9090",
		"HTTP_READ_HEADER_TIMEOUT": "2s",
		"HTTP_WRITE_TIMEOUT":       "12s",
		"HTTP_IDLE_TIMEOUT":        "90s",
		"DATABASE_URL":             "postgres://fake:fake@db/example",
		"PG_MAX_CONNS":             "8",
		"PG_MIN_CONNS":             "2",
		"PG_PING_TIMEOUT":          "4s",
		"REDIS_ADDR":               "localhost:6380",
		"REDIS_USERNAME":           "test-user",
		"REDIS_PASSWORD":           "fake-password",
		"REDIS_DB":                 "2",
		"REDIS_PING_TIMEOUT":       "1500ms",
		"STARTUP_TIMEOUT":          "20s",
	}
	for key, value := range env {
		t.Setenv(key, value)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("有效环境变量应加载成功：%v", err)
	}

	want := Config{
		Http: HttConfig{
			Addr:              ":9090",
			ReadHeaderTimeout: 2 * time.Second,
			WriteTimeout:      12 * time.Second,
			IdleTimeout:       90 * time.Second,
		},
		Postgres: PostgresConfig{
			DSN:         "postgres://fake:fake@db/example",
			MaxConns:    8,
			MinConns:    2,
			PingTimeout: 4 * time.Second,
		},
		Redis: RedisConfig{
			Addr:        "localhost:6380",
			Username:    "test-user",
			Password:    "fake-password",
			DB:          2,
			PingTimeout: 1500 * time.Millisecond,
		},
		StartupTimeout: 20 * time.Second,
	}

	if got != want {
		// 避免把整份配置（可能包含密码）打印到日志。
		t.Fatal("加载结果与受控环境变量不一致")
	}
}

func TestLoadInvalidEnv(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		value   string
		wantErr string
	}{
		{"缺少数据库连接串", "DATABASE_URL", "", "DATABASE_URL"},
		{"请求头超时格式错误", "HTTP_READ_HEADER_TIMEOUT", "bad", "HTTP_READ_HEADER_TIMEOUT"},
		{"写超时缺少单位", "HTTP_WRITE_TIMEOUT", "10", "HTTP_WRITE_TIMEOUT"},
		{"空闲超时格式错误", "HTTP_IDLE_TIMEOUT", "bad", "HTTP_IDLE_TIMEOUT"},
		{"数据库超时格式错误", "PG_PING_TIMEOUT", "bad", "PG_PING_TIMEOUT"},
		{"Redis超时格式错误", "REDIS_PING_TIMEOUT", "bad", "REDIS_PING_TIMEOUT"},
		{"启动超时格式错误", "STARTUP_TIMEOUT", "bad", "STARTUP_TIMEOUT"},
		{"启动超时为零", "STARTUP_TIMEOUT", "0s", "启动超时"},
		{"最大连接数不是整数", "PG_MAX_CONNS", "abc", "PG_MAX_CONNS"},
		{"最大连接数超出int32", "PG_MAX_CONNS", "2147483648", "PG_MAX_CONNS"},
		{"最大连接数为零", "PG_MAX_CONNS", "0", "PG_MAX_CONNS"},
		{"最小连接数超出int32", "PG_MIN_CONNS", "2147483648", "PG_MIN_CONNS"},
		{"最小连接数超过最大值", "PG_MIN_CONNS", "5", "PG_MIN_CONNS"},
		{"最小连接数为负数", "PG_MIN_CONNS", "-1", "PG_MIN_CONNS"},
		{"Redis编号不是整数", "REDIS_DB", "abc", "REDIS_DB"},
		{"Redis编号为负数", "REDIS_DB", "-1", "REDIS_DB"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prepareLoadEnv(t)
			t.Setenv(tt.key, tt.value)

			_, err := Load()
			if err == nil {
				t.Fatalf("应返回包含 %q 的错误，实际没有错误",
					tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("错误应包含 %q，实际为：%v",
					tt.wantErr, err)
			}
		})
	}
}
