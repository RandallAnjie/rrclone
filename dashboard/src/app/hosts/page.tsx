"use client";

import { Button, Chip, Input, Label, Modal, TextField, useOverlayState } from "@heroui/react";
import { useState } from "react";
import { MotionPage, Rise, Stagger } from "@/components/motion-ui";
import { PageHeader } from "@/components/page-header";
import { useHosts } from "@/components/host-provider";
import { normalizeHostUrl } from "@/lib/rc/format";
import type { Host } from "@/lib/rc/types";

type Draft = {
  name: string;
  url: string;
  user: string;
  pass: string;
};

const emptyDraft: Draft = {
  name: "",
  url: "http://127.0.0.1:5572",
  user: "",
  pass: "",
};

export default function HostsPage() {
  const { hosts, host, selectHost, addHost, updateHost, removeHost } = useHosts();
  const [draft, setDraft] = useState<Draft>(emptyDraft);
  const [editing, setEditing] = useState<Host | null>(null);
  const [error, setError] = useState<string | null>(null);
  const modal = useOverlayState();

  function openCreate() {
    setEditing(null);
    setDraft(emptyDraft);
    setError(null);
    modal.open();
  }

  function openEdit(item: Host) {
    setEditing(item);
    setDraft({
      name: item.name,
      url: item.url,
      user: item.user ?? "",
      pass: item.pass ?? "",
    });
    setError(null);
    modal.open();
  }

  function submit() {
    try {
      const payload = {
        name: draft.name,
        url: normalizeHostUrl(draft.url),
        user: draft.user,
        pass: draft.pass,
      };
      if (editing) {
        updateHost(editing.id, payload);
      } else {
        addHost(payload);
      }
      modal.close();
    } catch (err) {
      setError(err instanceof Error ? err.message : "保存失败");
    }
  }

  return (
    <MotionPage>
      <PageHeader
        kicker="节点"
        title="主机"
        description="现在默认管理本机 rclone RC。以后要管多台机器，只要再加一个 RC 地址。"
        actions={
          <Button variant="primary" onPress={openCreate}>
            添加主机
          </Button>
        }
      />
      <Stagger className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
        {hosts.map((item) => {
          const current = item.id === host.id;
          return (
            <Rise key={item.id}>
              <article className={`host-card h-full ${current ? "is-current" : ""}`}>
                <div className="mb-4 flex items-start justify-between gap-3">
                  <div className="flex items-center gap-3">
                    <span className="brand-mark" aria-hidden>
                      {(item.name.trim()[0] || "H").toUpperCase()}
                    </span>
                    <div>
                      <h2 className="text-base font-semibold tracking-tight">{item.name}</h2>
                      <p className="mono-meta mt-0.5">{item.url}</p>
                    </div>
                  </div>
                  <Chip color={current ? "success" : "default"} size="sm">
                    <Chip.Label>{current ? "当前" : "待命"}</Chip.Label>
                  </Chip>
                </div>
                <p className="mb-4 text-sm text-muted">认证：{item.user ? item.user : "无"}</p>
                <div className="flex flex-wrap gap-2">
                  <Button size="sm" variant="secondary" onPress={() => selectHost(item.id)}>
                    切换
                  </Button>
                  <Button size="sm" variant="tertiary" onPress={() => openEdit(item)}>
                    编辑
                  </Button>
                  <Button
                    size="sm"
                    variant="danger-soft"
                    isDisabled={Boolean(item.locked)}
                    onPress={() => removeHost(item.id)}
                  >
                    删除
                  </Button>
                </div>
              </article>
            </Rise>
          );
        })}
      </Stagger>

      <Modal state={modal}>
        <Modal.Backdrop>
          <Modal.Container>
            <Modal.Dialog>
              <Modal.Header>
                <Modal.Heading>{editing ? "编辑主机" : "添加主机"}</Modal.Heading>
              </Modal.Header>
              <Modal.Body className="space-y-4">
                <TextField
                  value={draft.name}
                  onChange={(value) => setDraft((prev) => ({ ...prev, name: value }))}
                >
                  <Label>名称</Label>
                  <Input placeholder="例如：书房 NAS" />
                </TextField>
                <TextField
                  value={draft.url}
                  isDisabled={Boolean(editing?.locked)}
                  onChange={(value) => setDraft((prev) => ({ ...prev, url: value }))}
                >
                  <Label>RC 地址</Label>
                  <Input placeholder="http://127.0.0.1:5572" />
                </TextField>
                <TextField
                  value={draft.user}
                  onChange={(value) => setDraft((prev) => ({ ...prev, user: value }))}
                >
                  <Label>用户名（可选）</Label>
                  <Input placeholder="rc 用户名" />
                </TextField>
                <TextField
                  value={draft.pass}
                  onChange={(value) => setDraft((prev) => ({ ...prev, pass: value }))}
                >
                  <Label>密码（可选）</Label>
                  <Input type="password" placeholder="rc 密码" />
                </TextField>
                {error ? <p className="text-sm text-danger">{error}</p> : null}
              </Modal.Body>
              <Modal.Footer>
                <Button variant="tertiary" onPress={modal.close}>
                  取消
                </Button>
                <Button variant="primary" onPress={submit}>
                  保存
                </Button>
              </Modal.Footer>
              <Modal.CloseTrigger />
            </Modal.Dialog>
          </Modal.Container>
        </Modal.Backdrop>
      </Modal>
    </MotionPage>
  );
}
