// 项目 store：列表 + 当前打开项目。
import { defineStore } from "pinia";
import { ref } from "vue";
import { api } from "@/api";
import { createReadVersion } from "@/composables/readGeneration";
import type { ID, Project, ProjectSummary } from "@/domain/types";

export const useProjectsStore = defineStore("projects", () => {
  const items = ref<ProjectSummary[]>([]);
  const current = ref<Project | null>(null);
  const loading = ref(false);

  // 列表与详情是两个独立读取域：打开详情不应作废列表回包，反之亦然。
  const listReads = createReadVersion();
  const openReads = createReadVersion();

  async function load(): Promise<void> {
    const token = listReads.begin();
    loading.value = true;
    try {
      const next = await api.listProjects();
      if (!listReads.current(token)) return;
      items.value = next;
    } finally {
      loading.value = false;
    }
  }

  async function open(id: ID): Promise<Project> {
    const token = openReads.begin();
    loading.value = true;
    try {
      const project = await api.getProject(id);
      // 旧 generation / 更早打开请求的晚回包不得覆盖当前项目。
      if (openReads.current(token)) current.value = project;
      return project;
    } finally {
      loading.value = false;
    }
  }

  async function create(name: string): Promise<Project> {
    const p = await api.createProject({ name });
    await load();
    return p;
  }

  async function rename(id: ID, name: string, editVersion?: number): Promise<Project> {
    const p = await api.updateProject(id, { name, editVersion });
    const summary = items.value.find((item) => item.id === id);
    if (summary) {
      summary.name = p.name;
      summary.editVersion = p.editVersion;
      summary.updatedAt = p.updatedAt;
    }
    if (current.value?.id === id) current.value = p;
    return p;
  }

  async function remove(id: ID): Promise<void> {
    await api.deleteProject(id);
    if (current.value?.id === id) current.value = null;
    await load();
  }

  return { items, current, loading, load, open, create, rename, remove };
});
