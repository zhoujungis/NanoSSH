package sshcore

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"nanossh/internal/testsshd"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// keyPairForTest 生成一把 ed25519 密钥，返回私钥对象（喂给内存钥匙串用）、
// 签名器（喂给 key 认证用）与公钥（喂给服务端授权用）。
func keyPairForTest(t *testing.T) (priv any, signer ssh.Signer, pub ssh.PublicKey) {
	t.Helper()
	priv, err := generatePrivateKey(AlgoEd25519)
	if err != nil {
		t.Fatalf("生成测试密钥失败: %v", err)
	}
	signer, err = ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("构造签名器失败: %v", err)
	}
	return priv, signer, signer.PublicKey()
}

// connectOptionsFor 拼一份指向本地靶机的连接参数。
// 主机密钥用"仅本次信任"，避免测试之间互相污染 known_hosts。
func connectOptionsFor(t *testing.T, srv *testsshd.Server, s Server) ConnectOptions {
	t.Helper()
	host, port := srv.HostPort()
	s.Host, s.Port = host, port
	return ConnectOptions{
		Server:         s,
		KnownHostsPath: t.TempDir() + "/known_hosts",
		HostKeyPrompt: func(HostKeyInfo, *HostKeyChange) (HostKeyDecision, error) {
			return HostKeyAcceptOnce, nil
		},
		Timeout: 5 * time.Second,
	}
}

func startRelay(t *testing.T, user string) *testsshd.Server {
	t.Helper()
	srv, err := testsshd.New(user, "unused-password")
	if err != nil {
		t.Fatalf("启动靶机失败: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

// TestConnectWithKey 覆盖 P2 的主验收路径：生成的密钥被服务端授权后即可登录。
func TestConnectWithKey(t *testing.T) {
	srv := startRelay(t, "tester")
	_, signer, pub := keyPairForTest(t)
	srv.AuthorizeKey(pub)

	opts := connectOptionsFor(t, srv, Server{Name: "靶机", User: "tester", Auth: AuthKey})
	opts.Auth = AuthInput{Signer: signer}

	client, err := Connect(context.Background(), opts)
	if err != nil {
		t.Fatalf("密钥登录失败: %v", err)
	}
	defer client.Close()

	if got := srv.LastAuth(); got != "publickey" {
		t.Fatalf("认证方式 = %q，期望 publickey", got)
	}
}

// TestConnectWithKeyRejectsUnauthorized 反向验证：未被授权的密钥必须失败。
// 少了这条，上面那条可能只是"服务端谁来都放行"。
func TestConnectWithKeyRejectsUnauthorized(t *testing.T) {
	srv := startRelay(t, "tester")
	_, signer, _ := keyPairForTest(t)

	opts := connectOptionsFor(t, srv, Server{Name: "靶机", User: "tester", Auth: AuthKey})
	opts.Auth = AuthInput{Signer: signer}

	if _, err := Connect(context.Background(), opts); err == nil {
		t.Fatal("未授权公钥应导致认证失败")
	}
}

// TestConnectWithAgent 用进程内 agent 替身（net.Pipe + agent.ServeAgent）验证 agent 认证路径：
// 不需要真的运行 ssh-agent，也不需要 Windows 命名管道。
func TestConnectWithAgent(t *testing.T) {
	srv := startRelay(t, "tester")
	priv, _, pub := keyPairForTest(t)
	srv.AuthorizeKey(pub)

	keyring := agent.NewKeyring()
	if err := keyring.Add(agent.AddedKey{PrivateKey: priv, Comment: "nanossh@test"}); err != nil {
		t.Fatalf("向内存钥匙串添加密钥失败: %v", err)
	}

	agentConn, clientConn := net.Pipe()
	t.Cleanup(func() { _ = clientConn.Close() })
	go func() { _ = agent.ServeAgent(keyring, agentConn) }()

	opts := connectOptionsFor(t, srv, Server{Name: "靶机", User: "tester", Auth: AuthAgent})
	opts.Auth = AuthInput{Agent: agent.NewClient(clientConn)}

	client, err := Connect(context.Background(), opts)
	if err != nil {
		t.Fatalf("agent 登录失败: %v", err)
	}
	defer client.Close()

	if got := srv.LastAuth(); got != "publickey" {
		t.Fatalf("认证方式 = %q，期望 publickey", got)
	}
}

// TestConnectWithEmptyAgent 确认 agent 里没有密钥时给的是明确文案，
// 而不是 ssh 库那句含糊的 "no supported methods remain"。
func TestConnectWithEmptyAgent(t *testing.T) {
	srv := startRelay(t, "tester")

	agentConn, clientConn := net.Pipe()
	t.Cleanup(func() { _ = clientConn.Close() })
	go func() { _ = agent.ServeAgent(agent.NewKeyring(), agentConn) }()

	opts := connectOptionsFor(t, srv, Server{Name: "靶机", User: "tester", Auth: AuthAgent})
	opts.Auth = AuthInput{Agent: agent.NewClient(clientConn)}

	_, err := Connect(context.Background(), opts)
	if err == nil {
		t.Fatal("空 agent 应导致认证失败")
	}
	if !strings.Contains(err.Error(), "中没有可用密钥") {
		t.Fatalf("空 agent 的报错文案不够明确: %v", err)
	}
}

// TestConnectKeyWithoutSigner 覆盖"服务器配了密钥认证但没给私钥"的报错。
func TestConnectKeyWithoutSigner(t *testing.T) {
	srv := startRelay(t, "tester")

	opts := connectOptionsFor(t, srv, Server{Name: "靶机", User: "tester", Auth: AuthKey})
	if _, err := Connect(context.Background(), opts); err == nil {
		t.Fatal("缺少私钥时应报错")
	}
}

// TestPasswordStillWorks 保证 P1 的密码路径没有被 Key / Agent 改造破坏。
func TestPasswordStillWorks(t *testing.T) {
	srv := startRelay(t, "tester")

	opts := connectOptionsFor(t, srv, Server{Name: "靶机", User: "tester", Auth: AuthPassword})
	opts.Auth = AuthInput{Password: "unused-password"}

	client, err := Connect(context.Background(), opts)
	if err != nil {
		t.Fatalf("密码登录失败: %v", err)
	}
	defer client.Close()

	if got := srv.LastAuth(); got != "password" {
		t.Fatalf("认证方式 = %q，期望 password", got)
	}
}
