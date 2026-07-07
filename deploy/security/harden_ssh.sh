#!/bin/bash
# FileSync 服务器 SSH 加固参考脚本
# 基于 aistudy.icu 安全评估报告的低风险问题 #8（SSH 版本较旧）
#
# 功能：
#   1. 禁用密码登录，仅允许密钥认证
#   2. 禁用 root 直接登录（通过 sudo 提权）
#   3. 修改默认 SSH 端口（可选，需同步安全组规则）
#   4. 安装 fail2ban 防暴力破解
#   5. 限制登录用户白名单
#
# 使用方式：
#   1. 先确保已有非 root 用户且已配置 sudo 权限
#   2. 上传该用户的公钥到 ~/.ssh/authorized_keys
#   3. 以 root 身份执行：bash harden_ssh.sh
#   4. 执行前请先备份 sshd_config：cp /etc/ssh/sshd_config /etc/ssh/sshd_config.bak
#
# 重要提示：
#   - 执行前请保持当前 SSH 会话，开一个新的会话测试，避免锁死自己
#   - 修改端口后需同步更新云厂商安全组规则
#   - 此脚本为参考模板，生产环境请根据实际情况调整

set -e

SSHD_CONFIG="/etc/ssh/sshd_config"
BACKUP="/etc/ssh/sshd_config.bak.$(date +%Y%m%d%H%M%S)"

echo "=== 步骤 1: 备份 sshd_config ==="
cp "$SSHD_CONFIG" "$BACKUP"
echo "已备份到 $BACKUP"

echo ""
echo "=== 步骤 2: 加固 SSH 配置 ==="
# 禁用密码登录（仅允许密钥认证）
sed -i 's/^#\?PasswordAuthentication.*/PasswordAuthentication no/' "$SSHD_CONFIG"
sed -i 's/^#\?ChallengeResponseAuthentication.*/ChallengeResponseAuthentication no/' "$SSHD_CONFIG"

# 禁用 root 直接登录（通过 sudo 提权更安全）
sed -i 's/^#\?PermitRootLogin.*/PermitRootLogin no/' "$SSHD_CONFIG"

# 禁用空密码
sed -i 's/^#\?PermitEmptyPasswords.*/PermitEmptyPasswords no/' "$SSHD_CONFIG"

# 限制最大认证尝试次数（3 次）
sed -i 's/^#\?MaxAuthTries.*/MaxAuthTries 3/' "$SSHD_CONFIG"

# 登录超时 30 秒
sed -i 's/^#\?LoginGraceTime.*/LoginGraceTime 30/' "$SSHD_CONFIG"

# 禁用 X11 转发（不需要）
sed -i 's/^#\?X11Forwarding.*/X11Forwarding no/' "$SSHD_CONFIG"

# 仅允许协议 2（SSH v1 已不安全，OpenSSH 7.6+ 默认禁用）
sed -i 's/^#\?Protocol.*/Protocol 2/' "$SSHD_CONFIG" 2>/dev/null || true

echo "SSH 配置已加固："
echo "  - PasswordAuthentication no（禁用密码登录）"
echo "  - PermitRootLogin no（禁用 root 直接登录）"
echo "  - MaxAuthTries 3（最大认证尝试 3 次）"
echo "  - LoginGraceTime 30（登录超时 30 秒）"
echo "  - X11Forwarding no（禁用 X11 转发）"

echo ""
echo "=== 步骤 3: 配置登录用户白名单（可选）==="
# 仅允许特定用户登录（取消注释并修改用户名）
# echo "AllowFiles your_username" >> "$SSHD_CONFIG"
# echo "已限制仅允许 your_username 登录"
echo "如需限制登录用户，请手动在 sshd_config 末尾添加：AllowUsers <username>"

echo ""
echo "=== 步骤 4: 修改默认端口（可选，默认注释）==="
# 修改 SSH 端口（需要同步更新安全组规则，谨慎操作）
# 新端口建议范围：10000-65535
# NEW_PORT=22222
# sed -i "s/^#\?Port.*/Port $NEW_PORT/" "$SSHD_CONFIG"
# echo "SSH 端口已修改为 $NEW_PORT，请同步更新云厂商安全组规则"
echo "如需修改默认端口，请取消脚本中的注释并执行"

echo ""
echo "=== 步骤 5: 安装 fail2ban（防暴力破解）==="
if command -v yum &> /dev/null; then
    # CentOS/RHEL
    yum install -y epel-release
    yum install -y fail2ban
    systemctl enable fail2ban
    systemctl start fail2ban
    echo "fail2ban 已安装并启动（CentOS/RHEL）"
elif command -v apt &> /dev/null; then
    # Debian/Ubuntu
    apt update
    apt install -y fail2ban
    systemctl enable fail2ban
    systemctl start fail2ban
    echo "fail2ban 已安装并启动（Debian/Ubuntu）"
else
    echo "警告：未识别的包管理器，请手动安装 fail2ban"
fi

echo ""
echo "=== 步骤 6: 配置 fail2ban SSH 规则 ==="
cat > /etc/fail2ban/jail.d/sshd.local << 'EOF'
[sshd]
enabled = true
port = ssh
filter = sshd
logpath = /var/log/secure
maxretry = 3
findtime = 300
bantime = 3600
EOF
systemctl restart fail2ban
echo "fail2ban SSH 规则已配置：3 次失败后封禁 1 小时"

echo ""
echo "=== 步骤 7: 验证 sshd 配置语法 ==="
if sshd -t; then
    echo "sshd 配置语法正确"
else
    echo "错误：sshd 配置语法错误，请检查 $SSHD_CONFIG"
    echo "可恢复备份：cp $BACKUP $SSHD_CONFIG"
    exit 1
fi

echo ""
echo "=== 步骤 8: 重启 sshd 服务 ==="
echo "警告：重启前请保持当前会话，开新会话测试登录"
echo "执行命令：systemctl restart sshd"
echo ""
echo "=== 加固完成 ==="
echo ""
echo "重要提示："
echo "1. 请勿关闭当前 SSH 会话"
echo "2. 打开新终端测试密钥登录是否正常"
echo "3. 确认正常后再关闭当前会话"
echo "4. 如出问题，可恢复备份：cp $BACKUP $SSHD_CONFIG && systemctl restart sshd"
echo ""
echo "加固摘要："
echo "  - 禁用密码登录，仅允许密钥认证"
echo "  - 禁用 root 直接登录"
echo "  - 最大认证尝试 3 次"
echo "  - 登录超时 30 秒"
echo "  - fail2ban：3 次失败封禁 1 小时"
