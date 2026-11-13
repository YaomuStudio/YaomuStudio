// 只读请求版本守卫：捕获 runtime generation/readyEpoch 与本地请求序号。
//
// 用途：Core 重启后 generation/epoch 变化，或在同一连接内又发起了更新的读取时，
// 旧请求的回包不得写回当前 store/页面。业务状态仍以 Core 为权威，这里只丢弃过期响应。
import { useRuntimeStore } from "@/stores/runtime";

export interface ReadToken {
  generation: number;
  epoch: number;
  seq: number;
}

export interface ReadVersion {
  begin(): ReadToken;
  current(token: ReadToken): boolean;
  /** 主动作废所有在飞读取（如停止订阅/断开项目）。 */
  invalidate(): void;
}

/** 当前宿主连接版本；供单飞读取判断是否可复用同一在飞请求。 */
export function currentReadVersion(): { generation: number; epoch: number } {
  const runtime = useRuntimeStore();
  return { generation: runtime.generation, epoch: runtime.readyEpoch };
}

/**
 * 每个 store 为独立读取域创建一个守卫；同一守卫内后发起的读取会使先前回包失效。
 * begin/current 延迟读取 Pinia store，避免模块加载时依赖未激活的 pinia。
 */
export function createReadVersion(): ReadVersion {
  let latest = 0;
  return {
    begin(): ReadToken {
      const runtime = useRuntimeStore();
      latest += 1;
      return { generation: runtime.generation, epoch: runtime.readyEpoch, seq: latest };
    },
    current(token: ReadToken): boolean {
      const runtime = useRuntimeStore();
      return (
        token.seq === latest &&
        token.generation === runtime.generation &&
        token.epoch === runtime.readyEpoch
      );
    },
    invalidate(): void {
      latest += 1;
    },
  };
}
