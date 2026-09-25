import { useEffect, useId, useRef, type ReactNode } from 'react'
import { createPortal } from 'react-dom'

// Open dialogs, innermost last: only it reacts to Escape.
const stack: string[] = []

interface ModalProps {
  title: ReactNode
  onClose: () => void
  children: ReactNode
  footer?: ReactNode
  /** Room for charts. */
  wide?: boolean
}

export function Modal({ title, onClose, children, footer, wide }: ModalProps) {
  const id = useId()
  const box = useRef<HTMLDivElement>(null)
  const close = useRef(onClose)
  useEffect(() => {
    close.current = onClose
  })

  useEffect(() => {
    stack.push(id)
    document.body.classList.add('modal-open')
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && stack[stack.length - 1] === id) close.current()
    }
    document.addEventListener('keydown', onKey)
    box.current?.querySelector<HTMLElement>('input:not([type=radio]), select, textarea, button.primary')?.focus()
    return () => {
      document.removeEventListener('keydown', onKey)
      stack.splice(stack.indexOf(id), 1)
      if (!stack.length) document.body.classList.remove('modal-open')
    }
  }, [id])

  return createPortal(
    <div
      className="backdrop"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose()
      }}
    >
      <div
        className={wide ? 'modal wide' : 'modal'}
        role="dialog"
        aria-modal="true"
        aria-labelledby={`${id}-title`}
        ref={box}
      >
        <h3 id={`${id}-title`}>{title}</h3>
        {children}
        {footer && <div className="footer">{footer}</div>}
      </div>
    </div>,
    document.body,
  )
}
