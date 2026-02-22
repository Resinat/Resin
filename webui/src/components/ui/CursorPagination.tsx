import { Button } from "./Button";
import { Select } from "./Select";

type CursorPaginationProps = {
    pageIndex: number;
    hasMore: boolean;
    pageSize: number;
    pageSizeOptions?: readonly number[];
    onPageSizeChange: (pageSize: number) => void;
    onPrev: () => void;
    onNext: () => void;
};

export function CursorPagination({
    pageIndex,
    hasMore,
    pageSize,
    pageSizeOptions = [20, 50, 100, 200],
    onPageSizeChange,
    onPrev,
    onNext,
}: CursorPaginationProps) {
    return (
        <div className="nodes-pagination">
            <p className="nodes-pagination-meta">
                第 {pageIndex + 1} 页 · {hasMore ? "存在下一页" : "无更多数据"}
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

                <Button variant="secondary" size="sm" onClick={onPrev} disabled={pageIndex <= 0}>
                    上一页
                </Button>
                <Button variant="secondary" size="sm" onClick={onNext} disabled={!hasMore}>
                    下一页
                </Button>
            </div>
        </div>
    );
}
