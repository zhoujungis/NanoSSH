//go:build windows

package store

import (
	"errors"
	"testing"
)

// TestWincredRoundTrip 是真的往 Windows 凭据管理器里写一条再读回、再删除。
//
// 这条测试必须碰真实后端：wincred 是本项目唯一"我们没有自己实现"的持久化组件，
// 用替身测它等于什么都没测。target 每次随机生成，跑完即删，不留残留。
func TestWincredRoundTrip(t *testing.T) {
	s := NewSecretStore()
	target := "NanoSSH/test/" + NewID()
	t.Cleanup(func() { _ = s.Delete(target) })

	// 未写入前必须是"未找到"，而不是空字符串——
	// 否则上层无法区分"没有密码"与"密码是空的"。
	if _, err := s.Get(target); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("未写入时应返回 ErrSecretNotFound，实际: %v", err)
	}

	const secret = "s3cret-含中文-🔑"
	if err := s.Set(target, secret); err != nil {
		t.Fatalf("写入凭据失败: %v", err)
	}

	got, err := s.Get(target)
	if err != nil {
		t.Fatalf("读回凭据失败: %v", err)
	}
	if got != secret {
		t.Fatalf("读回的值 = %q，期望 %q", got, secret)
	}

	// 覆盖写入同样要生效。
	if err := s.Set(target, "second"); err != nil {
		t.Fatalf("覆盖凭据失败: %v", err)
	}
	if got, _ := s.Get(target); got != "second" {
		t.Fatalf("覆盖后读回 = %q，期望 second", got)
	}

	if err := s.Delete(target); err != nil {
		t.Fatalf("删除凭据失败: %v", err)
	}
	if _, err := s.Get(target); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("删除后应返回 ErrSecretNotFound，实际: %v", err)
	}
	// 幂等：重复删除不应报错。
	if err := s.Delete(target); err != nil {
		t.Fatalf("重复删除应幂等，实际: %v", err)
	}
}

// TestMemSecretStoreContract 用同一套契约校验内存实现，
// 保证测试里替掉 wincred 时行为一致。
func TestMemSecretStoreContract(t *testing.T) {
	s := NewMemSecretStore()
	target := PasswordTarget("01TEST")

	if _, err := s.Get(target); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("未写入时应返回 ErrSecretNotFound，实际: %v", err)
	}
	if err := s.Set(target, "pw"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(target); got != "pw" {
		t.Fatalf("读回 = %q，期望 pw", got)
	}
	if err := s.Delete(target); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(target); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("删除后应返回 ErrSecretNotFound，实际: %v", err)
	}
}
