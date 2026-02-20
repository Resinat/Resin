import { useMemo } from "react";
import { Button } from "./Button";
import { Select } from "./Select";

type OffsetPaginationProps = {
  page: number;
  totalPages: number;
  totalItems: number;
  pageSize: number;
  pageSizeOptions: readonly number[];
  onPageChange: (page: number) => void;
  onPageSizeChange: (pageSize: number) => void;
};

export function OffsetPagination({
  page,
  totalPages,
  totalItems,
  pageSize,
  pageSizeOptions,
  onPageChange,
  onPageSizeChange,
}: OffsetPaginationProps) {
  const normalizedTotalPages = Math.max(1, totalPages);
  const normalizedPage = Math.min(Math.max(page, 0), normalizedTotalPages - 1);
  const pageStart = totalItems === 0 ? 0 : normalizedPage * pageSize + 1;
  const pageEnd = Math.min((normalizedPage + 1) * pageSize, totalItems);

  const pageOptions = useMemo(() => {
    return Array.from({ length: normalizedTotalPages }, (_, index) => index);
  }, [normalizedTotalPages]);

  const jumpTo = (nextPage: number) => {
    const bounded = Math.min(Math.max(nextPage, 0), normalizedTotalPages - 1);
    onPageChange(bounded);
  };

  return (
    <div className="nodes-pagination">
      <p className="nodes-pagination-meta">
        第 {normalizedPage + 1} / {normalizedTotalPages} 页 · 显示 {pageStart}-{pageEnd} / {totalItems}
      </p>
      <div className="nodes-pagination-controls">
        <label className="nodes-page-size">
          <span>每页</span>
          <Select value={String(pageSize)} onChange={(event) => onPageSizeChange(Number(event.target.value))}>
            {pageSizeOptions.map((size) => (
              <option key={size} value={size}>
                {size}
              </option>
            ))}
          </Select>
        </label>

        <label className="nodes-page-jump">
          <span>跳至</span>
          <Select value={String(normalizedPage)} onChange={(event) => jumpTo(Number(event.target.value))} aria-label="选择页码">
            {pageOptions.map((index) => (
              <option key={index} value={index}>
                {index + 1}
              </option>
            ))}
          </Select>
          <span>页</span>
        </label>

        <Button variant="secondary" size="sm" onClick={() => jumpTo(normalizedPage - 1)} disabled={normalizedPage <= 0}>
          上一页
        </Button>
        <Button
          variant="secondary"
          size="sm"
          onClick={() => jumpTo(normalizedPage + 1)}
          disabled={normalizedPage >= normalizedTotalPages - 1}
        >
          下一页
        </Button>
      </div>
    </div>
  );
}
