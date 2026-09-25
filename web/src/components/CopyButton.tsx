import { useTranslation } from 'react-i18next'
import { useToast } from '../context/feedback'
import { copyText } from '../lib/clipboard'

export function CopyButton({ text, label }: { text: string; label?: string }) {
  const { t } = useTranslation()
  const toast = useToast()
  const copy = async () => {
    toast((await copyText(text)) ? t('common.copied') : t('common.copyManually', { text }))
  }
  return (
    <button type="button" className="small" onClick={() => void copy()}>
      {label ?? t('common.copy')}
    </button>
  )
}
