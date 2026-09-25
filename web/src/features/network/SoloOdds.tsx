import { useTranslation } from 'react-i18next'
import type { CoinView, NetworkStatus, Pool, TimedStatus } from '../../api/types'
import { locale } from '../../i18n'
import { parseGoDuration } from '../../lib/duration'
import { formatChance, formatDifficulty, formatDuration, formatHashrateHs, formatLongDuration } from '../../lib/format'
import { blockChance, blockRate } from '../../lib/odds'

const hour = 3600
const day = 24 * hour

interface SoloOddsProps {
  network: NetworkStatus
  hashrateTHs: number
  pools: Pool[]
  timed?: TimedStatus
}

/** Network difficulty of every coin and the farm's chances to find a block solo. */
export function SoloOdds({ network, hashrateTHs, pools, timed }: SoloOddsProps) {
  const { t } = useTranslation()
  const loc = locale()
  const hs = hashrateTHs * 1e12
  const mined = new Set(pools.map((p) => p.coin).filter(Boolean))
  // Coins of the configured pools first, then the rest in revenue order.
  const coins = [...network.coins].sort((a, b) => Number(mined.has(b.tag)) - Number(mined.has(a.tag)))

  const chance = (c: CoinView, seconds: number, share = 1) => formatChance(blockChance(hs * share, c.difficulty_now, seconds), t, loc)
  const mean = (c: CoinView, share = 1) => formatLongDuration(1 / blockRate(hs * share, c.difficulty_now), t, loc)
  // Rounded to a tenth first, so a tiny change shows as 0% and not −0%.
  const vs24 = (c: CoinView) => (c.difficulty > 0 ? Math.round((c.difficulty_now / c.difficulty - 1) * 1000) / 10 : 0)

  // With timed switching the farm is on the timer pool only part of the time.
  const target = timed?.mode === 'on' ? pools.find((p) => p.id === timed.target) : undefined
  const targetCoin = target && coins.find((c) => c.tag === target.coin)
  const share = timed ? parseGoDuration(timed.duration) / parseGoDuration(timed.period) : 0

  if (network.error) return <div className="panel muted small">{t('network.noData', { error: network.error })}</div>
  return (
    <div className="panel network">
      <p className="small muted">{hs > 0 ? t('network.farm', { hashrate: formatHashrateHs(hs) }) : t('network.noHashrate')}</p>
      <div className="table-wrap">
        <table className="responsive">
          <thead>
            <tr>
              <th>{t('network.coin')}</th>
              <th className="num">{t('network.difficulty')}</th>
              <th className="num">{t('network.vs24')}</th>
              <th className="num">{t('network.reward')}</th>
              <th className="num">{t('network.hour')}</th>
              <th className="num">{t('network.day')}</th>
              <th className="num">{t('network.mean')}</th>
            </tr>
          </thead>
          <tbody>
            {coins.map((c) => {
              const d = vs24(c)
              return (
                <tr key={c.tag} className={mined.has(c.tag) ? undefined : 'muted'}>
                  <td className="primary">
                    <b>{c.tag}</b> {mined.has(c.tag) && <span className="badge">{t('network.mined')}</span>}
                  </td>
                  <td data-label={t('network.difficulty')} className="num">
                    {formatDifficulty(c.difficulty_now)}
                  </td>
                  <td data-label={t('network.vs24')} className={`num ${d <= -1 ? 'lvl-ok' : d >= 1 ? 'lvl-warn' : ''}`}>
                    {`${d > 0 ? '+' : d < 0 ? '−' : ''}${Math.abs(d).toLocaleString(loc, { maximumFractionDigits: 1 })}%`}
                  </td>
                  <td data-label={t('network.reward')} className="num">
                    {c.block_reward.toLocaleString(loc, { maximumSignificantDigits: 5 })} {c.tag}
                    {c.price_usd > 0 && (
                      <div className="small muted">
                        {(c.block_reward * c.price_usd).toLocaleString(loc, {
                          style: 'currency',
                          currency: 'USD',
                          currencyDisplay: 'narrowSymbol',
                          maximumFractionDigits: 0,
                        })}
                      </div>
                    )}
                  </td>
                  <td data-label={t('network.hour')} className="num">
                    {hs > 0 ? chance(c, hour) : '—'}
                  </td>
                  <td data-label={t('network.day')} className="num">
                    {hs > 0 ? chance(c, day) : '—'}
                  </td>
                  <td data-label={t('network.mean')} className="num">
                    {hs > 0 ? mean(c) : '—'}
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
      {target && targetCoin && hs > 0 && timed && (
        <p className="small">
          {t('network.timed', {
            pool: target.name,
            coin: targetCoin.tag,
            length: formatDuration(parseGoDuration(timed.duration), t),
            every: formatDuration(parseGoDuration(timed.period), t),
            chance: chance(targetCoin, day, share),
            mean: mean(targetCoin, share),
          })}
        </p>
      )}
      <p className="small muted">{t('network.note')}</p>
    </div>
  )
}
