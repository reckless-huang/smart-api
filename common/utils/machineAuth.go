// @Author sunwenbo
// 2024/8/28 15:34
package utils

import (
	"bytes"
	"fmt"
	"golang.org/x/crypto/ssh"
	"net"
	"time"
)

type MachineConn struct{}

func (c *MachineConn) TestConnection(authType string, ip string, port int, username, password, privateKey string) error {
	switch authType {
	case "1":
		// 通过用户名和密码进行SSH连接测试
		return c.testPasswordAuth(ip, port, username, password)
	case "2":
		// 通过公私钥进行SSH连接测试
		return c.testKeyAuth(ip, port, username, privateKey)
	default:
		return fmt.Errorf("invalid authentication type")
	}
}

func (c *MachineConn) testPasswordAuth(ip string, port int, username, password string) error {
	sshConfig := &ssh.ClientConfig{
		User: username,
		Auth: []ssh.AuthMethod{
			ssh.Password(password),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}

	// 拼接ip和端口
	host := fmt.Sprintf("%s:%d", ip, port)
	client, err := ssh.Dial("tcp", host, sshConfig)
	if err != nil {
		return err
	}
	defer client.Close()

	return nil
}

// 使用私钥进行登陆时，首先需要将公钥放在内容放到 .ssh/authorized_keys 文件中，
// 然后需要将私钥提供给客户端，客户端就可以拿着私钥进行认证登陆 ssh -i id_rsa  root@10.50.183.112 -p 52829
func (c *MachineConn) testKeyAuth(ip string, port int, username, privateKey string) error {

	// 解析私钥
	signer, err := ssh.ParsePrivateKey([]byte(privateKey))
	if err != nil {
		return fmt.Errorf("failed to parse private key: %v", err)
	}

	sshConfig := &ssh.ClientConfig{
		User: username,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}

	// 拼接ip和端口
	host := fmt.Sprintf("%s:%d", ip, port)
	client, err := ssh.Dial("tcp", host, sshConfig)
	if err != nil {
		return fmt.Errorf("failed to connect: %v", err)
	}
	defer client.Close()

	return nil
}

func (c *MachineConn) testTCPPort(ip string, port int) error {
	address := fmt.Sprintf("%s:%d", ip, port)
	timeout := 3 * time.Second // 设置超时时间

	conn, err := net.DialTimeout("tcp", address, timeout)
	if err != nil {
		return fmt.Errorf("failed to connect to %s: %v", address, err)
	}
	defer conn.Close()

	return nil
}

func (c *MachineConn) ExecuteCommand(client *ssh.Client, command string) (string, string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", "", fmt.Errorf("failed to create session: %v", err)
	}
	defer session.Close()

	var stdoutBuf, stderrBuf bytes.Buffer
	session.Stdout = &stdoutBuf
	session.Stderr = &stderrBuf

	if err := session.Run(command); err != nil {
		return stdoutBuf.String(), stderrBuf.String(), fmt.Errorf("failed to execute command: %v", err)
	}

	return stdoutBuf.String(), stderrBuf.String(), nil
}
