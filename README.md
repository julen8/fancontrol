# Fan Control

面向 Supermicro X10/X11 风格 BMC 的 Linux 风扇控制守护进程。程序读取 CPU 与所选硬盘温度，通过 IPMI zone 0/1 分别控制 CPU 风扇和系统风扇，并提供单页 Web 控制台。

## 功能

- CPU 温度自动发现：`coretemp`、`k10temp` 或 `zenpower`
- 使用 `lsblk` 和 `/dev/disk/by-id` 自动发现稳定的硬盘标识
- 使用 `smartctl -n standby` 读取温度且尽量不唤醒休眠盘
- CPU 与 HDD 独立使用分段温度曲线或固定风速
- 超温和连续传感器读取失败时应用安全风速
- 保持 BMC FULL 模式，正常退出时应用配置的退出风速
- TOML 配置、Web 热更新、简单 Session 登录
- 静态页面嵌入 Go 二进制，无需额外前端运行环境

## 前提

- Linux
- Go 1.24 或更高版本（仅编译需要）
- `ipmitool`
- `smartmontools`（提供 `smartctl`）
- Supermicro 主板支持以下 X10/X11 风扇控制指令：

```text
0x30 0x45 0x00
0x30 0x45 0x01 0x01
0x30 0x70 0x66 0x01 <zone> <speed>
```

在生产使用前，必须确认主板命令、风扇 zone 和最低稳定转速。不要与其他风扇控制程序同时运行。

## 编译

```bash
go build -o fancontrol ./cmd/fancontrol
```

## 运行

```bash
sudo ./fancontrol --config /etc/fancontrol/fancontrol.toml
```

指定文件不存在时，程序会创建父目录和权限为 `0600` 的默认 TOML，并在标准输出显示一次初始账号。默认用户名为 `admin`，密码随机生成。

浏览器访问：

```text
http://服务器地址:8080
```

其他命令：

```bash
./fancontrol --config ./fancontrol.toml --check-config
./fancontrol --config ./fancontrol.toml --reset-password
./fancontrol --version
```

配置中的监听地址在进程启动时读取；修改 `[server].listen` 后需要重启服务。其他控制参数保存后立即生效。

## 安全提示

Web 默认监听 `0.0.0.0:8080` 并启用账号认证，但当前版本使用明文 HTTP。只应在可信局域网中开放，并使用主机防火墙阻止公网访问。密码以 bcrypt 哈希保存，Session 保存在内存中，程序重启后需要重新登录。

## systemd

将二进制安装到 `/usr/local/bin/fancontrol`，然后参考 [packaging/fancontrol.service](packaging/fancontrol.service)。服务需要 root 权限以访问 IPMI 与 SMART 数据。

```bash
sudo install -m 0755 fancontrol /usr/local/bin/fancontrol
sudo install -m 0644 packaging/fancontrol.service /etc/systemd/system/fancontrol.service
sudo systemctl daemon-reload
sudo systemctl enable --now fancontrol
sudo journalctl -u fancontrol
```

首次启动密码可从该次 `journalctl` 输出查看。登录后应立即修改密码。
