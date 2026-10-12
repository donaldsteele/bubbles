<script lang="ts">
  interface Props {
    id: string;
    label: string;
    value: string;
    type?: 'text' | 'password';
    error?: string;
    hint?: string;
    autocomplete?: import('svelte/elements').FullAutoFill | null;
    required?: boolean;
    disabled?: boolean;
  }

  let {
    id,
    label,
    value = $bindable(),
    type = 'text',
    error = '',
    hint = '',
    autocomplete,
    required = false,
    disabled = false,
  }: Props = $props();

  const hintId = $derived(`${id}-hint`);
  const errorId = $derived(`${id}-error`);
  const describedBy = $derived(
    [hint ? hintId : '', error ? errorId : ''].filter(Boolean).join(' ') || undefined,
  );
</script>

<div class="field">
  <label for={id}>{label}</label>
  <input
    {id}
    {type}
    {autocomplete}
    {required}
    {disabled}
    bind:value
    aria-invalid={error ? 'true' : undefined}
    aria-describedby={describedBy}
  />
  {#if hint}<span class="hint" id={hintId}>{hint}</span>{/if}
  {#if error}<span class="error" id={errorId} role="alert">{error}</span>{/if}
</div>
