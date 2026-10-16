#!/usr/bin/env bash
# ensure-stream.sh —— 压测战役推流源复用：SRS 容器/源视频/常驻推流缺哪个补哪个，ffprobe 验证。
# 用法（WSL 内绝对路径）：bash /mnt/c/Users/macao/.zcode/workspace/live/recmaestro/docs/iterations/V0.1.0/生产就绪压测/scripts/ensure-stream.sh
# 推流进程随 WSL VM 关机会死（保活由 ensure-stack.sh 负责）；SRS 容器端点损坏时直接删了重 run（无状态）。
set -u
SRC="$HOME/recmaestro-e2e-src.mp4"   # 源视频放 WSL home，跨关机持久
RTMP_LOCAL="rtmp://127.0.0.1:21935/live/e2e"          # 主机侧探测地址
NET=deploy_default                                     # compose 栈网络（worker 用容器名访问 SRS）

# 1) SRS 容器：running 则不动；否则删旧重 run（Exited 容器 start 可能因网络端点损坏失败）
running=$(docker inspect -f '{{.State.Running}}' recmaestro-srs-e2e 2>/dev/null)
if [ "$running" = "true" ]; then
  echo "[SRS] recmaestro-srs-e2e running"
else
  echo "[SRS] 不在 running 状态（$running），重建容器"
  docker rm -f recmaestro-srs-e2e >/dev/null 2>&1
  docker run -d --name recmaestro-srs-e2e --network "$NET" -p 21935:1935 ossrs/srs:5 >/dev/null \
    || { echo "[SRS] docker run 失败" >&2; exit 1; }
  sleep 4
  echo "[SRS] 已重建并启动"
fi

# 2) 源视频：不在则用 testsrc 生成约 5 分钟（1280x720 h264+aac）
if [ -f "$SRC" ]; then
  echo "[源] $SRC 已存在"
else
  echo "[源] 缺失，ffmpeg testsrc 生成 300s"
  ffmpeg -y -v error -f lavfi -i testsrc=size=1280x720:rate=25 \
    -f lavfi -i sine=frequency=440:beep_factor=2 -t 300 \
    -c:v libx264 -preset ultrafast -pix_fmt yuv420p -c:a aac -shortest "$SRC" \
    || { echo "[源] 生成失败" >&2; exit 1; }
  echo "[源] 已生成 $SRC"
fi

# 3) 常驻推流：进程在则不动；不在则 setsid nohup 挂后台循环推流
if pgrep -f 'ffmpeg -re -stream_loop' >/dev/null 2>&1; then
  echo "[推流] 常驻推流在跑 (pid $(pgrep -f 'ffmpeg -re -stream_loop' | head -1))"
else
  echo "[推流] 不在，挂常驻循环推流 -> $RTMP_LOCAL"
  setsid nohup ffmpeg -re -stream_loop -1 -i "$SRC" -c copy -f flv "$RTMP_LOCAL" \
    >/tmp/e2e_push.log 2>&1 < /dev/null &
  sleep 8
  pgrep -f 'ffmpeg -re -stream_loop' >/dev/null 2>&1 \
    || { echo "[推流] 启动失败，看 /tmp/e2e_push.log" >&2; exit 1; }
  echo "[推流] 已挂常驻推流 (pid $(pgrep -f 'ffmpeg -re -stream_loop' | head -1))"
fi

# 4) ffprobe 验证 rtmp 流可读
timeout 20 ffprobe -v error -show_entries stream=codec_name,codec_type -of csv=p=0 "$RTMP_LOCAL" \
  && echo "[验证] ffprobe 读取 $RTMP_LOCAL 成功" \
  || { echo "[验证] ffprobe 读流失败（推流是否真正起来？看 /tmp/e2e_push.log）" >&2; exit 1; }
