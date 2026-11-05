// 访客留言的挂载位置（R-281 第 4 段接上）：留言挂在需求、决策、证据、阶段这些具体对象上。
// 第 3 段只留位置，什么都不显示；第 4 段在这里放「留言」按钮，点了在页面底部打开写给这个对象的输入框。
export type CommentTarget = { kind: 'req' | 'dec' | 'evidence' | 'stage'; ref: string; req?: string }

export function CommentSlot(_: { target: CommentTarget; className?: string }) {
  return null
}
