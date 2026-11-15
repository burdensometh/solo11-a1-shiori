<script setup lang="ts">
import { computed, ref } from 'vue';
import { useI18n } from 'vue-i18n';
import type { ModelBookmarkDTO } from '@/client';
import { useBookmarksStore } from '@/stores/bookmarks';
import { useToast } from '@/composables/useToast';
import Button from './ui/Button.vue';

const props = defineProps<{
  bookmark: ModelBookmarkDTO;
}>();

const emit = defineEmits<{
  retried: [bookmark: ModelBookmarkDTO];
}>();

const { t } = useI18n();
const bookmarksStore = useBookmarksStore();
const { success, error: showError } = useToast();

const isRetrying = ref(false);

const hasFailed = computed(() => props.bookmark.processingStatus === 'failed');

const failureMessage = computed(() => {
  const stepKey = props.bookmark.processingFailedStep
    ? `bookmarks.processing_step.${props.bookmark.processingFailedStep}`
    : '';
  const step = stepKey && t(stepKey) !== stepKey ? t(stepKey) : '';
  const message = props.bookmark.processingError || '';
  if (step && message) {
    return t('bookmarks.processing_failure_detail', { step, message });
  }
  return message;
});

const retry = async () => {
  if (!props.bookmark.id || isRetrying.value) {
    return;
  }

  isRetrying.value = true;
  try {
    const refreshed = await bookmarksStore.retryBookmarkProcessing(
      props.bookmark.id
    );
    emit('retried', refreshed);
    if (refreshed.processingStatus === 'failed') {
      showError(
        t('bookmarks.processing_retry_failed'),
        refreshed.processingError || ''
      );
    } else {
      success(t('bookmarks.processing_retry_success'));
    }
  } catch (err) {
    console.error('Retry processing failed:', err);
    showError(t('bookmarks.processing_retry_failed'));
  } finally {
    isRetrying.value = false;
  }
};
</script>

<template>
  <div
    v-if="hasFailed"
    role="alert"
    class="bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded-lg p-4 flex flex-col sm:flex-row sm:items-center sm:justify-between gap-3"
  >
    <div class="min-w-0">
      <p class="text-sm font-medium text-red-800 dark:text-red-200">
        {{ t('bookmarks.processing_failed_title') }}
      </p>
      <p
        v-if="failureMessage"
        class="text-sm text-red-700 dark:text-red-300 mt-1 break-words"
      >
        {{ failureMessage }}
      </p>
    </div>
    <Button
      variant="primary"
      class="flex-shrink-0"
      :loading="isRetrying"
      @click="retry"
    >
      {{
        isRetrying
          ? t('bookmarks.processing_retrying')
          : t('bookmarks.processing_retry')
      }}
    </Button>
  </div>
</template>
