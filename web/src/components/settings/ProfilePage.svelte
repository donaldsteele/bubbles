<script lang="ts">
  import { api, call } from '../../lib/api/client';
  import type { Schemas } from '../../lib/api/client';
  import { failureFrom, validateRequired } from '../../lib/forms';
  import { t } from '../../lib/i18n/en';
  import { session } from '../../lib/state/session.svelte';
  import { users } from '../../lib/state/users.svelte';
  import FormField from '../forms/FormField.svelte';

  let displayName = $state(session.me?.display_name ?? '');
  let statusText = $state(session.me?.status_text ?? '');
  let avatarId = $state<string | null>(session.me?.avatar_file_id ?? null);
  let errors = $state<Record<string, string>>({});
  let formError = $state('');
  let saved = $state(false);
  let submitting = $state(false);
  let uploading = $state(false);

  async function chooseAvatar(e: Event) {
    const input = e.currentTarget as HTMLInputElement;
    const file = input.files?.[0];
    input.value = '';
    if (!file) return;
    uploading = true;
    formError = '';
    try {
      const form = new FormData();
      form.append('file', file);
      const res = await fetch('/api/v1/files', {
        method: 'POST',
        body: form,
        credentials: 'same-origin',
      });
      if (!res.ok) throw new Error('upload');
      const f = (await res.json()) as Schemas['File'];
      if (f.category !== 'image') {
        formError = t.rich.uploadServer;
        return;
      }
      avatarId = f.id;
      saved = false;
    } catch {
      formError = t.rich.uploadFailed;
    } finally {
      uploading = false;
    }
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    errors = { display_name: validateRequired(displayName) };
    formError = '';
    saved = false;
    if (errors['display_name']) return;
    submitting = true;
    try {
      const me = await call(
        api.PATCH('/me', {
          body: {
            display_name: displayName.trim(),
            status_text: statusText.trim(),
            avatar_file_id: avatarId,
          },
        }),
      );
      session.setMe(me);
      users.upsert(me);
      saved = true;
    } catch (err) {
      const f = failureFrom(err);
      errors = f.fields;
      formError = f.form;
    } finally {
      submitting = false;
    }
  }
</script>

<section aria-labelledby="profile-title">
  <h2 id="profile-title">{t.rich.profileTitle}</h2>
  <form onsubmit={submit} novalidate>
    {#if formError}<p class="form-error" role="alert">{formError}</p>{/if}
    <div class="avatar-row">
      {#if avatarId}
        <img
          class="avatar"
          src={`/api/v1/files/${avatarId}/avatar?thumb=1`}
          alt=""
          width="64"
          height="64"
        />
      {:else}
        <div class="avatar ph" aria-hidden="true">
          {(displayName || session.me?.username || '?').charAt(0).toUpperCase()}
        </div>
      {/if}
      <label class="btn">
        {t.rich.avatarChoose}
        <input
          class="visually-hidden"
          type="file"
          accept="image/png,image/jpeg,image/webp,image/gif"
          disabled={uploading}
          onchange={chooseAvatar}
        />
      </label>
      {#if avatarId}
        <button
          class="btn"
          type="button"
          onclick={() => {
            avatarId = null;
            saved = false;
          }}>{t.rich.avatarRemove}</button
        >
      {/if}
    </div>
    <FormField
      id="profile-name"
      label={t.rich.displayName}
      bind:value={displayName}
      error={errors['display_name']}
      required
      disabled={submitting}
    />
    <FormField
      id="profile-status"
      label={t.rich.statusText}
      bind:value={statusText}
      error={errors['status_text']}
      disabled={submitting}
    />
    <button class="btn primary" type="submit" disabled={submitting || uploading}>
      {submitting ? t.common.submitting : t.rich.saveProfile}
    </button>
    {#if saved}<p role="status">{t.rich.saved}</p>{/if}
  </form>
</section>

<style>
  h2 {
    font-size: 18px;
  }
  .avatar-row {
    display: flex;
    align-items: center;
    gap: var(--s3);
    margin-bottom: var(--s3);
  }
  .avatar {
    width: 64px;
    height: 64px;
    border-radius: var(--radius);
    object-fit: cover;
    border: 1px solid var(--line);
  }
  .ph {
    display: grid;
    place-items: center;
    font-size: 28px;
    font-weight: 700;
    background: var(--bg-elev2);
  }
  label.btn:focus-within {
    outline: 2px solid var(--focus);
    outline-offset: 2px;
  }
</style>
