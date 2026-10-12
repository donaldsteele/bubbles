<script lang="ts">
  import { onMount } from 'svelte';
  import { api, call, type Schemas } from '../../lib/api/client';
  import { LIMITS } from '../../lib/admin/validate';
  import { failureFrom } from '../../lib/forms';
  import { fmt, t } from '../../lib/i18n/en';

  type Channel = Schemas['Channel'];

  interface Row {
    ch: Channel;
    retention: string;
    error: string;
  }

  let rows = $state<Row[]>([]);
  let loading = $state(true);
  let error = $state('');

  function toRow(ch: Channel): Row {
    return {
      ch,
      retention: ch.retention_days === null ? '' : String(ch.retention_days),
      error: '',
    };
  }

  async function load() {
    try {
      rows = (await call(api.GET('/admin/channels'))).items.map(toRow);
      error = '';
    } catch (err) {
      error = failureFrom(err).form;
    } finally {
      loading = false;
    }
  }
  onMount(() => void load());

  async function patch(row: Row, body: Schemas['UpdateAdminChannelRequest']) {
    row.error = '';
    error = '';
    try {
      const ch = await call(
        api.PATCH('/admin/channels/{id}', { params: { path: { id: row.ch.id } }, body }),
      );
      rows = rows.map((r) => (r.ch.id === ch.id ? toRow(ch) : r));
    } catch (err) {
      const f = failureFrom(err);
      row.error = f.fields['retention_days'] ?? f.fields['is_default'] ?? f.form;
    }
  }

  function saveRetention(row: Row) {
    const v = row.retention.trim();
    if (v === '') return patch(row, { retention_days: null });
    const n = /^\d{1,5}$/.test(v) ? Number(v) : 0;
    if (n < 1 || n > LIMITS.retentionMaxDays) {
      row.error = t.admin.errRetention;
      return;
    }
    return patch(row, { retention_days: n });
  }
</script>

<section aria-labelledby="ch-title">
  <h2 id="ch-title">{t.admin.tabChannels}</h2>
  <p class="lead">{t.admin.channelsHelp}</p>
  {#if error}<p class="form-error" role="alert">{error}</p>{/if}
  {#if loading}<p role="status">{t.app.loading}</p>{/if}
  <div class="table-wrap">
    <table>
      <caption class="visually-hidden">{t.admin.tabChannels}</caption>
      <thead>
        <tr>
          <th scope="col">{t.admin.channel}</th>
          <th scope="col">{t.admin.members}</th>
          <th scope="col">{t.admin.retentionDays}</th>
          <th scope="col">{t.admin.defaultChannel}</th>
          <th scope="col">{t.admin.actions}</th>
        </tr>
      </thead>
      <tbody>
        {#each rows as row (row.ch.id)}
          <tr>
            <th scope="row">
              #{row.ch.name}
              <span class="chip"
                >{row.ch.kind === 'private' ? t.admin.private : t.admin.public}</span
              >
              {#if row.ch.archived_at}<span class="chip">{t.admin.archived}</span>{/if}
            </th>
            <td>{row.ch.member_count}</td>
            <td>
              <div class="row">
                <label class="visually-hidden" for={`ret-${row.ch.id}`}
                  >{fmt(t.admin.retentionOf, { channel: row.ch.name ?? '' })}</label
                >
                <input
                  id={`ret-${row.ch.id}`}
                  inputmode="numeric"
                  size="5"
                  placeholder={t.admin.forever}
                  bind:value={row.retention}
                  aria-invalid={row.error ? 'true' : undefined}
                />
                <button class="btn small" type="button" onclick={() => void saveRetention(row)}
                  >{t.common.save}</button
                >
              </div>
              {#if row.error}<p class="field-error" role="alert">{row.error}</p>{/if}
            </td>
            <td>
              {#if row.ch.kind === 'public'}
                <label class="inline">
                  <input
                    type="checkbox"
                    checked={row.ch.is_default}
                    onchange={(e) => void patch(row, { is_default: e.currentTarget.checked })}
                  />
                  <span class="visually-hidden"
                    >{fmt(t.admin.defaultOf, { channel: row.ch.name ?? '' })}</span
                  >
                </label>
              {:else}
                {t.admin.none}
              {/if}
            </td>
            <td>
              <button
                class="btn small"
                type="button"
                onclick={() => void patch(row, { archived: !row.ch.archived_at })}
              >
                {row.ch.archived_at ? t.admin.unarchive : t.admin.archive}
              </button>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
</section>
