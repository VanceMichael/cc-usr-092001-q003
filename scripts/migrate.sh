#!/bin/sh
# 初始化本地数据目录。状态快照与事件流在首次写入时自动创建。
set -eu
: "${DATABASE_PATH:=data/state.json}"
mkdir -p "${DATABASE_PATH%/*}"
printf '%s\n' "数据目录已就绪：${DATABASE_PATH%/*}（快照 ${DATABASE_PATH}，事件流 ${DATABASE_PATH}.events.jsonl）"
