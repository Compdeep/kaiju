---
name: healthcheck
description: "System security audit, host hardening, and health checks. Use when the user asks for security review, system status, vulnerability scanning, or hardening recommendations."
---

## When to Use

Use when the user asks to:
- Audit system security posture
- Check for open ports or running services
- Review firewall rules
- Check for software updates
- Assess SSH configuration
- Review user accounts and permissions
- Run a general system health check

## Planning Guidance

### Quick system health check

Plan parallel checks — these are all independent and read-only:

1. `bash` — `uname -a && uptime` (system info)
2. `bash` — `df -h` (disk usage, parallel)
3. `bash` — `free -h` (memory, parallel)
4. `bash` — `ps aux --sort=-%mem | head -15` (top processes, parallel)
5. `sysinfo` — system information from built-in tool (parallel)

### Security audit

Plan parallel scans across independent areas:

1. `bash` — `ss -tlnp` (open listening ports)
2. `bash` — `cat /etc/ssh/sshd_config | grep -E "^(PermitRoot|PasswordAuth|Port|AllowUsers)"` (SSH config, parallel)
3. `bash` — `sudo ufw status verbose 2>/dev/null || sudo iptables -L -n 2>/dev/null` (firewall, parallel)
4. `bash` — `cat /etc/passwd | awk -F: '$3 == 0 {print $1}'` (root-level accounts, parallel)
5. `bash` — `find /home -name "authorized_keys" -type f 2>/dev/null` (SSH keys, parallel)

### Check for updates

1. `bash` — detect package manager and check:
   ```
   apt list --upgradable 2>/dev/null || yum check-update 2>/dev/null || brew outdated 2>/dev/null
   ```

### Network exposure scan

1. `bash` — `ss -tlnp` (listening ports)
2. `bash` — `curl -s ifconfig.me` (external IP, parallel)
3. `bash` — `ip route show default` (gateway info, parallel)

### Docker health

1. `bash` — `docker ps --format "table {{.Names}}\t{{.Status}}\t{{.Ports}}"` (running containers)
2. `bash` — `docker images --format "table {{.Repository}}\t{{.Tag}}\t{{.Size}}"` (images, parallel)

### Full hardening review

Combine all the above in parallel, then synthesize recommendations in the aggregator.

### What NOT to do

- Don't run destructive commands (rm, kill, stop services) during a health check — this is observe-only
- Don't modify firewall rules, SSH config, or system files without explicit user confirmation
- Don't plan sequential checks for independent subsystems — parallelize everything
- Don't assume root access — check permissions first, suggest `sudo` only when needed
- Don't skip the synthesis step — raw output needs interpretation
