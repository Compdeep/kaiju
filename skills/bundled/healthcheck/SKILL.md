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

Several of these checks have a tool of their own — `sysinfo`, `disk_usage`, `process_list`, `net_info`, `env_list`. Prefer them over the equivalent shell command: they return typed fields a later step can reference, and they pass through the execution gate as observation. Use `bash` for what has no tool.

### Quick system health check

These are independent and read-only, so plan them with no reference between them and they run at the same time:

1. `sysinfo` — what this machine is
2. `disk_usage` — free and used space
3. `process_list` — what is running
4. `bash` — `uptime && free -h` for load and memory

### Security audit

Plan parallel scans across independent areas:

1. `net_info` — the machine's addresses and interfaces, and `bash` — `ss -tlnp` for what is listening
2. `bash` — `grep -E "^(PermitRoot|PasswordAuth|Port|AllowUsers)" /etc/ssh/sshd_config`
3. `bash` — `ufw status verbose 2>/dev/null || iptables -L -n 2>/dev/null` for the firewall. Both usually need root; if they return nothing, report that the firewall could not be read rather than that no rules exist.
4. `bash` — `awk -F: '$3 == 0 {print $1}' /etc/passwd` for accounts with uid 0
5. `bash` — `find /home -name authorized_keys -type f 2>/dev/null` for installed SSH keys

Nothing links these, so they run at the same time.

### Check for updates

1. `bash` — detect package manager and check:
   ```
   apt list --upgradable 2>/dev/null || yum check-update 2>/dev/null || brew outdated 2>/dev/null
   ```

### Network exposure scan

1. `bash` — `ss -tlnp` for listening ports
2. `net_info` — the machine's own addresses and routes
3. `bash` — `curl -s ifconfig.me` for the address the machine presents to the internet. This asks an outside service, so plan it only when external exposure is the question.

### Docker health

1. `bash` — `docker ps --format "table {{.Names}}\t{{.Status}}\t{{.Ports}}"` for running containers
2. `bash` — `docker images --format "table {{.Repository}}\t{{.Tag}}\t{{.Size}}"` for images

Neither needs the other, so they run at the same time.

### Full hardening review

Combine all the above in parallel, then synthesize recommendations in the aggregator.

### What NOT to do

- Don't run destructive commands (rm, kill, stop services) during a health check — this is observe-only
- Don't modify firewall rules, SSH config, or system files without explicit user confirmation
- Don't plan sequential checks for independent subsystems — parallelize everything
- Don't assume root access. A check that needs it and does not have it returns nothing, and nothing is not a clean result — report that it could not be read. Suggest `sudo` to the user rather than putting it in a command.
- Don't skip the synthesis step — raw output needs interpretation
