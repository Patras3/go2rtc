# go2rtc with Keepalived - High Availability Docker Image

Docker image combining go2rtc with keepalived for high availability (HA) setups using VRRP.

**Note**: This image requires keepalive configuration for HA mode. Without `keepalived.conf`, it runs go2rtc only.

## Features

- ✅ go2rtc with QVR camera fix (improved RTSP timeout handling)
- ✅ keepalived for VRRP-based high availability
- ✅ Intel hardware acceleration (amd64 only)
- ✅ Lightweight Alpine Linux base
- ✅ Proper init system (tini)

## Quick Start

### Option 1: Standalone Mode (no HA)

```bash
docker run -d \
  --name go2rtc \
  -v /path/to/config:/config \
  -p 1984:1984 \
  -p 8554:8554 \
  -p 8555:8555/tcp \
  -p 8555:8555/udp \
  pdadas/go2rtc-keepalived:latest
```

**Required**: `/path/to/config/go2rtc.yaml`

### Option 2: High Availability Mode (with keepalived)

**Requires keepalive configuration!**

```bash
# Master Node
docker run -d \
  --name go2rtc-master \
  --cap-add=NET_ADMIN \
  --cap-add=NET_BROADCAST \
  --net=host \
  -v /path/to/config:/config \
  pdadas/go2rtc-keepalived:latest

# Backup Node
docker run -d \
  --name go2rtc-backup \
  --cap-add=NET_ADMIN \
  --cap-add=NET_BROADCAST \
  --net=host \
  -v /path/to/config:/config \
  pdadas/go2rtc-keepalived:latest
```

**Required files**:
- `/path/to/config/go2rtc.yaml` (go2rtc configuration)
- `/path/to/config/keepalived.conf` (keepalived configuration)

## Configuration

### go2rtc.yaml (required)

```yaml
api:
  listen: ":1984"

rtsp:
  listen: ":8554"

webrtc:
  listen: ":8555"

streams:
  camera1:
    # QVR cameras: use #timeout=100 for stable connection
    - rtsp://admin:password@192.168.1.100:554/stream#timeout=100
```

### keepalived.conf (optional, required for HA)

Example configuration for high availability:

```
vrrp_script check_go2rtc {
    script "/usr/bin/curl -f http://localhost:1984/api/streams || exit 1"
    interval 5
    weight 2
}

vrrp_instance VI_1 {
    state MASTER              # MASTER on first node, BACKUP on second
    interface eth0            # Your network interface name
    virtual_router_id 51
    priority 100              # 100 on MASTER, 90 on BACKUP
    advert_int 1

    authentication {
        auth_type PASS
        auth_pass SecurePassword123
    }

    virtual_ipaddress {
        192.168.1.100/24      # Your virtual IP address
    }

    track_script {
        check_go2rtc
    }

    notify /usr/local/bin/vrrp_notify.sh
}
```

## How It Works

### Startup Sequence

1. **entrypoint.sh** runs first:
   - Checks for `/config/keepalived.conf`
   - If found: starts keepalived in background
   - If not found: skips keepalived (standalone mode)
   - Starts go2rtc in foreground

2. **keepalived** (HA mode only):
   - Monitors go2rtc health every 5 seconds
   - If go2rtc fails, demotes node to BACKUP
   - Virtual IP automatically moves to backup node

3. **vrrp_notify.sh** handles state changes:
   - **MASTER**: Node acquired virtual IP (no action)
   - **BACKUP**: Node lost MASTER role → restarts go2rtc
   - **FAULT**: Health check failed → restarts go2rtc

## Ports

| Port | Protocol | Description |
|------|----------|-------------|
| 1984 | TCP | go2rtc API & Web UI |
| 8554 | TCP | RTSP server |
| 8555 | TCP/UDP | WebRTC |

## Requirements for HA Mode

### Docker Capabilities
```bash
--cap-add=NET_ADMIN         # Required for VRRP
--cap-add=NET_BROADCAST     # Required for multicast
--net=host                  # Required for virtual IP
```

### Network Requirements
- VRRP protocol (IP protocol 112) must not be blocked by firewall
- Multicast address `224.0.0.18` must be accessible
- All HA nodes must be on same L2 network segment

## Building from Source

```bash
# Build go2rtc binary
CGO_ENABLED=0 go build -ldflags "-s -w" -trimpath

# Build Docker image
docker build -f docker/Dockerfile -t go2rtc-keepalived:latest .

# For ARM64
docker build -f docker/Dockerfile --build-arg TARGETARCH=arm64 -t go2rtc-keepalived:arm64 .
```

## Troubleshooting

### Check if keepalived is running
```bash
docker exec go2rtc-master ps aux | grep keepalived
```

### View logs
```bash
docker logs -f go2rtc-master
```

### Test failover
1. Verify which node has virtual IP:
   ```bash
   ip addr show | grep 192.168.1.100
   ```

2. Stop MASTER node:
   ```bash
   docker stop go2rtc-master
   ```

3. Virtual IP should appear on BACKUP within 1-3 seconds

### Common Issues

**"keepalived exited or failed to start"**
- Check `keepalived.conf` syntax
- Ensure NET_ADMIN capability is set
- Verify network interface name in config

**"No /config/keepalived.conf found"**
- This is normal if running in standalone mode
- Image works without keepalived configuration
- Add `keepalived.conf` only if you need HA

**VRRP not working**
- Firewall blocking protocol 112
- Multicast disabled on network
- Wrong network interface name in config
- Nodes not on same network segment

## Tags

- `latest`: Latest build from master branch
- `<commit-sha>`: Specific commit version (e.g., `51b4b24`)
- `old`: Legacy version (1.8.5, deprecated)

## Source Code

- GitHub: https://github.com/Patras3/go2rtc
- Original project: https://github.com/AlexxIT/go2rtc

## License

Same as go2rtc project (MIT).
