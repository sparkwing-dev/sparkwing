package objectguard

import (
	"context"
	"fmt"

	awsmiddleware "github.com/aws/aws-sdk-go-v2/aws/middleware"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go/middleware"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// MiddlewareID names the budget middleware in an S3 client's stack.
const MiddlewareID = "SparkwingObjectStoreBudget"

// WithBudget returns an S3 client option that spends l's budget on every
// billed attempt.
//
// The middleware sits inside the SDK's retry loop rather than around the
// API call, because the object store bills each attempt the retryer
// sends, not each call the caller makes. A client built without it is
// unbudgeted.
func WithBudget(l *Limiter) func(*s3.Options) {
	return func(o *s3.Options) {
		if l == nil {
			return
		}
		o.APIOptions = append(o.APIOptions, func(stack *middleware.Stack) error {
			return stack.Finalize.Insert(&budgetMiddleware{limiter: l}, "Retry", middleware.After)
		})
	}
}

type budgetMiddleware struct{ limiter *Limiter }

func (m *budgetMiddleware) ID() string { return MiddlewareID }

func (m *budgetMiddleware) HandleFinalize(
	ctx context.Context,
	in middleware.FinalizeInput,
	next middleware.FinalizeHandler,
) (middleware.FinalizeOutput, middleware.Metadata, error) {
	class := ClassForOperation(awsmiddleware.GetOperationName(ctx))
	if err := m.limiter.Allow(class); err != nil {
		return middleware.FinalizeOutput{}, middleware.Metadata{}, fmt.Errorf("%s: %w", awsmiddleware.GetOperationName(ctx), err)
	}
	out, metadata, err := next.HandleFinalize(ctx, in)
	if err == nil {
		m.limiter.RecordWrite(class, requestBodyBytes(in))
	}
	return out, metadata, err
}

// perf: the bucket total grows by what this attempt carried, so the ceiling
// never lists the bucket to learn a write happened.
func requestBodyBytes(in middleware.FinalizeInput) int64 {
	req, ok := in.Request.(*smithyhttp.Request)
	if !ok || req.Request == nil || req.ContentLength < 0 {
		return 0
	}
	return req.ContentLength
}

// ClassForOperation maps an S3 operation name to the class the object
// store bills it under. An operation this build does not know is billed
// as a write, which is the expensive guess and the safe one.
func ClassForOperation(operation string) Class {
	switch operation {
	case "GetObject", "HeadObject", "HeadBucket", "GetBucketLocation":
		return ClassGet
	case "ListObjectsV2", "ListObjects", "ListBuckets", "ListMultipartUploads", "ListParts":
		return ClassList
	case "DeleteObject", "DeleteObjects":
		return ClassDelete
	default:
		return ClassPut
	}
}
